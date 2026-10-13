package server

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/DeepFal/PortRelay/core/protocol"
)

const (
	DefaultPortMin = 33000
	DefaultPortMax = 34000

	// authTimeout 是新连接完成鉴权的最长等待时间，超时直接断开
	authTimeout = 10 * time.Second
)

// agentSession 表示一个已建立的 Agent 连接，控制流写入与指标推送都绑定在该会话上
type agentSession struct {
	sess    *yamux.Session
	writer  *bufio.Writer
	writeMu sync.Mutex
	done    chan struct{}
}

func (s *agentSession) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	data = append(data, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.writer.Write(data)
	_ = s.writer.Flush()
}

// Manager manages all active tunnels on the server node
type Manager struct {
	portMin int
	portMax int
	token   string

	// PublicHost 显示在授权页面上的中转访问地址；为空时使用访问者打开链接时的主机名
	PublicHost string
	// TrustProxyHeaders 为 true 时 /auth 信任 X-Forwarded-For / X-Real-IP（仅在前置可信反向代理时开启）
	TrustProxyHeaders bool

	mu      sync.RWMutex
	active  *agentSession
	tunnels map[string]*Tunnel
	portMap map[int]string // port -> tunnelID

	secEvents   []protocol.SecurityEvent
	secEventsMu sync.Mutex

	state *stateStore // 临时放通、邀请链接、审批申请
}

// NewManager creates a manager
func NewManager(portMin, portMax int, token string) *Manager {
	if portMin <= 0 {
		portMin = DefaultPortMin
	}
	if portMax <= 0 {
		portMax = DefaultPortMax
	}
	return &Manager{
		portMin:   portMin,
		portMax:   portMax,
		token:     token,
		tunnels:   make(map[string]*Tunnel),
		portMap:   make(map[int]string),
		secEvents: make([]protocol.SecurityEvent, 0),
		state:     newStateStore(),
	}
}

// HandleSession handles an incoming connection from the Mac agent.
// 只有在控制流上通过 token 鉴权后，该连接才会成为当前活跃会话。
func (m *Manager) HandleSession(rawConn net.Conn) error {
	defer rawConn.Close()

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.LogOutput = io.Discard

	session, err := yamux.Server(rawConn, cfg)
	if err != nil {
		return fmt.Errorf("yamux server init error: %w", err)
	}
	defer session.Close()

	// 非 Agent 连接（端口扫描、探活等）在超时内打不开控制流，直接丢弃
	type acceptResult struct {
		stream net.Conn
		err    error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		st, err := session.AcceptStream()
		acceptCh <- acceptResult{st, err}
	}()
	var controlStream net.Conn
	select {
	case res := <-acceptCh:
		if res.err != nil {
			return fmt.Errorf("failed to accept control stream: %w", res.err)
		}
		controlStream = res.stream
	case <-time.After(authTimeout):
		return errors.New("timed out waiting for control stream")
	}
	defer controlStream.Close()

	as := &agentSession{
		sess:   session,
		writer: bufio.NewWriter(controlStream),
		done:   make(chan struct{}),
	}
	defer close(as.done)

	reader := bufio.NewReader(controlStream)

	// 1. 第一条消息必须是鉴权
	_ = controlStream.SetReadDeadline(time.Now().Add(authTimeout))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read auth message: %w", err)
	}
	_ = controlStream.SetReadDeadline(time.Time{})

	var auth protocol.AuthMessage
	if err := json.Unmarshal(line, &auth); err != nil || auth.Type != protocol.TypeAuth {
		as.send(protocol.AuthRespMessage{Type: protocol.TypeAuthResp, Success: false, Error: "first message must be auth"})
		return errors.New("first control message is not auth")
	}
	if !m.checkToken(auth.Token) {
		as.send(protocol.AuthRespMessage{Type: protocol.TypeAuthResp, Success: false, Error: "invalid authentication token"})
		return errors.New("invalid authentication token")
	}

	// 2. 鉴权通过：替换旧会话，旧会话的通道全部释放，由新 Agent 重新注册
	m.activate(as)
	defer m.deactivate(as)
	as.send(protocol.AuthRespMessage{Type: protocol.TypeAuthResp, Success: true})
	log.Printf("[Server] Agent %s authenticated, control stream established", rawConn.RemoteAddr())

	go m.metricsLoop(as)

	// 3. 控制消息循环
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("[Server] control stream read error: %v", err)
			}
			return nil
		}
		if len(line) == 0 {
			continue
		}

		var base protocol.BaseMessage
		if err := json.Unmarshal(line, &base); err != nil {
			log.Printf("[Server] malformed control message: %v", err)
			continue
		}
		m.handleControlMessage(as, base.Type, line)
	}
}

func (m *Manager) checkToken(token string) bool {
	if m.token == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(m.token)) == 1
}

func (m *Manager) activate(as *agentSession) {
	m.mu.Lock()
	old := m.active
	m.active = as
	m.closeAllTunnelsLocked()
	m.mu.Unlock()

	if old != nil {
		log.Printf("[Server] New agent session replaces the previous one")
		_ = old.sess.Close()
	}
}

// deactivate 只在该会话仍是活跃会话时清理，避免旧会话迟到的断开把新会话的通道清掉
func (m *Manager) deactivate(as *agentSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != as {
		return
	}
	m.active = nil
	m.closeAllTunnelsLocked()
	log.Printf("[Server] Agent disconnected, cleaned up all tunnels")
}

func (m *Manager) closeAllTunnelsLocked() {
	for _, t := range m.tunnels {
		t.Close()
	}
	m.tunnels = make(map[string]*Tunnel)
	m.portMap = make(map[int]string)
}

func (m *Manager) handleControlMessage(as *agentSession, msgType string, raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] PANIC while handling control message %q: %v", msgType, r)
		}
	}()
	switch msgType {
	case protocol.TypeRegisterTunnel:
		var msg protocol.RegisterTunnelMessage
		if json.Unmarshal(raw, &msg) == nil {
			as.send(m.registerTunnel(as, msg.Tunnel))
		}

	case protocol.TypeUnregister:
		var msg protocol.UnregisterMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.unregisterTunnel(as, msg.TunnelID)
			as.send(protocol.UnregisterRespMessage{Type: protocol.TypeUnregisterResp, TunnelID: msg.TunnelID, Success: true})
		}

	case protocol.TypeUpdateTunnel:
		var msg protocol.UpdateTunnelMessage
		if json.Unmarshal(raw, &msg) == nil {
			as.send(m.updateTunnel(as, msg.Tunnel))
		}

	case protocol.TypeRevokeLease:
		var msg protocol.RevokeLeaseMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.revokeLease(msg.TunnelID, msg.IP)
		}

	case protocol.TypeCreateInvite:
		var msg protocol.CreateInviteMessage
		if json.Unmarshal(raw, &msg) == nil {
			as.send(m.createInvite(msg))
		}

	case protocol.TypeRevokeInvite:
		var msg protocol.RevokeInviteMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.revokeInvite(msg.TunnelID, msg.Code)
		}

	case protocol.TypeDecideRequest:
		var msg protocol.DecideRequestMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.decideRequest(msg.TunnelID, msg.RequestID, msg.Approve)
		}

	case protocol.TypeKillConn:
		var msg protocol.KillConnMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.killConn(msg.ConnID)
		}

	case protocol.TypePing:
		as.send(protocol.BaseMessage{Type: protocol.TypePong})
	}
}

func registerResp(cfg protocol.TunnelConfig, err error) protocol.RegisterRespMessage {
	resp := protocol.RegisterRespMessage{
		Type:     protocol.TypeRegisterResp,
		TunnelID: cfg.ID,
		Success:  err == nil,
		Port:     cfg.ListenPort,
	}
	if err != nil {
		resp.Error = err.Error()
	}
	return resp
}

// checkPortLocked 校验端口范围以及是否被其它通道占用
func (m *Manager) checkPortLocked(cfg protocol.TunnelConfig) error {
	if cfg.ListenPort < m.portMin || cfg.ListenPort > m.portMax {
		return fmt.Errorf("port %d out of allowed range [%d, %d]", cfg.ListenPort, m.portMin, m.portMax)
	}
	if existingID, inUse := m.portMap[cfg.ListenPort]; inUse && existingID != cfg.ID {
		return fmt.Errorf("port %d already in use by tunnel %s", cfg.ListenPort, existingID)
	}
	return nil
}

func (m *Manager) registerTunnel(as *agentSession, cfg protocol.TunnelConfig) protocol.RegisterRespMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != as {
		return registerResp(cfg, errors.New("agent session is no longer active"))
	}
	if err := m.checkPortLocked(cfg); err != nil {
		return registerResp(cfg, err)
	}

	// 同 ID 重新注册：先关闭旧监听（端口可能相同，必须先释放），统计与临时白名单沿用
	old := m.tunnels[cfg.ID]
	if old != nil {
		old.Close()
		delete(m.portMap, old.ListenPort())
		delete(m.tunnels, cfg.ID)
	}

	t, err := newTunnel(cfg, as.sess, m.addSecEvent, m.onClientConnect, old)
	if err != nil {
		return registerResp(cfg, err)
	}
	if old == nil {
		// 服务端重启或 Agent 重连后，恢复该通道尚未过期的临时放通
		t.restoreDynamicLeases(m.state.leaseExpiries(cfg.ID))
	}
	m.tunnels[cfg.ID] = t
	m.portMap[cfg.ListenPort] = cfg.ID

	log.Printf("[Server] Registered tunnel '%s' listening on 0.0.0.0:%d", cfg.Name, cfg.ListenPort)
	return registerResp(cfg, nil)
}

func (m *Manager) unregisterTunnel(as *agentSession, tunnelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != as {
		return
	}
	// 主动关闭/删除通道时，放通、邀请链接与待审批申请一并失效（断线重连不会走到这里）
	m.state.forgetTunnel(tunnelID)
	if t, exists := m.tunnels[tunnelID]; exists {
		t.Close()
		delete(m.portMap, t.ListenPort())
		delete(m.tunnels, tunnelID)
		log.Printf("[Server] Unregistered tunnel '%s' (port %d freed)", t.Name(), t.ListenPort())
	}
}

// updateTunnel 端口不变时原地更新（不断开现有连接）；端口变化时先绑定新端口再关闭旧端口，失败则保留旧通道
func (m *Manager) updateTunnel(as *agentSession, cfg protocol.TunnelConfig) protocol.RegisterRespMessage {
	m.mu.Lock()
	t, exists := m.tunnels[cfg.ID]
	if !exists || m.active != as {
		m.mu.Unlock()
		return m.registerTunnel(as, cfg)
	}
	defer m.mu.Unlock()

	if t.ListenPort() == cfg.ListenPort {
		t.UpdateConfig(cfg)
		log.Printf("[Server] Updated tunnel '%s' whitelist/metadata", cfg.Name)
		return registerResp(cfg, nil)
	}

	if err := m.checkPortLocked(cfg); err != nil {
		return registerResp(cfg, err)
	}
	nt, err := newTunnel(cfg, as.sess, m.addSecEvent, m.onClientConnect, t)
	if err != nil {
		return registerResp(cfg, err)
	}
	t.Close()
	delete(m.portMap, t.ListenPort())
	m.tunnels[cfg.ID] = nt
	m.portMap[cfg.ListenPort] = cfg.ID
	log.Printf("[Server] Moved tunnel '%s' from port %d to %d", cfg.Name, t.ListenPort(), cfg.ListenPort)
	return registerResp(cfg, nil)
}

func (m *Manager) killConn(connID string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, t := range m.tunnels {
		if t.KillConn(connID) {
			log.Printf("[Server] Killed connection %s", connID)
			break
		}
	}
}

func (m *Manager) revokeLease(tunnelID, ip string) {
	m.mu.RLock()
	t := m.tunnels[tunnelID]
	m.mu.RUnlock()
	if t == nil {
		return
	}
	revoked, kicked := t.RevokeDynamic(ip)
	m.state.removeLease(tunnelID, ip)
	target := ip
	if target == "" {
		target = "all"
	}
	log.Printf("[WebAuth] Revoked %d temporary lease(s) (%s) on tunnel %s, disconnected %d client(s)",
		revoked, target, t.Name(), kicked)
}

func (m *Manager) tunnel(id string) *Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tunnels[id]
}

func (m *Manager) createInvite(msg protocol.CreateInviteMessage) protocol.InviteRespMessage {
	resp := protocol.InviteRespMessage{Type: protocol.TypeInviteResp, RequestID: msg.RequestID}
	t := m.tunnel(msg.TunnelID)
	switch {
	case t == nil:
		resp.Error = "通道未开启，无法生成邀请链接"
	case msg.ValidMinutes <= 0 || msg.ValidMinutes > 30*24*60:
		resp.Error = "邀请有效期须在 1 分钟到 30 天之间"
	case msg.MaxUses < 0:
		resp.Error = "可用设备数不能为负数"
	case !protocol.ValidLeaseDuration(msg.LeaseDuration):
		resp.Error = "无效的授权时长"
	default:
		resp.Invite = m.state.createInvite(msg.TunnelID, truncateRunes(msg.Note, 64), time.Duration(msg.ValidMinutes)*time.Minute, msg.MaxUses, msg.LeaseDuration, msg.RequireApproval)
		resp.Success = true
		log.Printf("[WebAuth] Created invite for tunnel %s (valid %dm, max %d device(s))", t.Name(), msg.ValidMinutes, msg.MaxUses)
	}
	return resp
}

func (m *Manager) revokeInvite(tunnelID, code string) {
	ips := m.state.revokeInvite(tunnelID, code)
	kicked := 0
	if t := m.tunnel(tunnelID); t != nil {
		for _, ip := range ips {
			_, k := t.RevokeDynamic(ip)
			kicked += k
		}
	}
	log.Printf("[WebAuth] Revoked invite on tunnel %s: %d lease(s) removed, %d client(s) disconnected", tunnelID, len(ips), kicked)
}

func (m *Manager) decideRequest(tunnelID, requestID string, approve bool) {
	t := m.tunnel(tunnelID)
	if t == nil {
		return
	}
	r, exp, err := m.state.decide(tunnelID, requestID, approve, t.Snapshot().ExpiresAt)
	if err != nil {
		return
	}
	if approve {
		t.AddDynamicWhitelist(r.IP, exp)
	}
	log.Printf("[WebAuth] Request from %s (%s) on tunnel %s: %s", r.Name, r.IP, t.Name(), r.Status)
}

// onClientConnect starts the countdown of a device's lease on its first connection
func (m *Manager) onClientConnect(tunnelID, ip string) {
	t := m.tunnel(tunnelID)
	if t == nil {
		return
	}
	if exp, started := m.state.startLease(tunnelID, ip, t.Snapshot().ExpiresAt, time.Now()); started {
		t.AddDynamicWhitelist(ip, exp)
		log.Printf("[WebAuth] %s connected for the first time on tunnel %s, access valid until %s", ip, t.Name(), exp.Format("01-02 15:04"))
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

func (m *Manager) addSecEvent(ev protocol.SecurityEvent) {
	m.secEventsMu.Lock()
	defer m.secEventsMu.Unlock()
	m.secEvents = append(m.secEvents, ev)
	if len(m.secEvents) > 100 {
		m.secEvents = m.secEvents[len(m.secEvents)-100:]
	}
}

func (m *Manager) metricsLoop(as *agentSession) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-as.done:
			return
		case <-ticker.C:
		}

		m.mu.RLock()
		if m.active != as {
			m.mu.RUnlock()
			return
		}
		tunnelMetricsList := make([]protocol.TunnelMetrics, 0, len(m.tunnels))
		allActiveConns := make([]protocol.ActiveConnInfo, 0)
		for _, t := range m.tunnels {
			// 授权到期（或白名单变更）后，已建立的连接也要断开，“授权后可访问 X”才名副其实
			for _, addr := range t.EnforceAccess() {
				log.Printf("[Tunnel %d] access expired, disconnected %s", t.ListenPort(), addr)
			}
			t.Tick()
			tm, conns := t.Metrics()
			tm.DynamicLeases = m.state.leaseList(tm.TunnelID)
			tm.Invites = m.state.inviteList(tm.TunnelID)
			tm.Pending = m.state.pendingList(tm.TunnelID)
			tunnelMetricsList = append(tunnelMetricsList, tm)
			allActiveConns = append(allActiveConns, conns...)
		}
		m.mu.RUnlock()

		m.secEventsMu.Lock()
		secCopy := make([]protocol.SecurityEvent, len(m.secEvents))
		copy(secCopy, m.secEvents)
		m.secEventsMu.Unlock()

		as.send(protocol.MetricsMessage{
			Type:           protocol.TypeMetrics,
			Tunnels:        tunnelMetricsList,
			ActiveConns:    allActiveConns,
			SecurityEvents: secCopy,
		})
	}
}

type httpResponseWriter struct {
	conn       net.Conn
	header     http.Header
	statusCode int
	wroteHdr   bool
}

func (w *httpResponseWriter) Header() http.Header {
	return w.header
}

func (w *httpResponseWriter) WriteHeader(statusCode int) {
	if !w.wroteHdr {
		w.statusCode = statusCode
		w.wroteHdr = true
		fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode))
		_ = w.header.Write(w.conn)
		fmt.Fprintf(w.conn, "\r\n")
	}
}

func (w *httpResponseWriter) Write(b []byte) (int, error) {
	if !w.wroteHdr {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(b)
}

// HandleHTTP handles incoming HTTP requests (such as /auth?code=... and /health)
func (m *Manager) HandleHTTP(conn net.Conn) {
	defer conn.Close()
	// 单个请求出错不能拖垮整个服务进程
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[Server] PANIC while handling HTTP request: %v", r)
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	req.RemoteAddr = conn.RemoteAddr().String()
	if m.TrustProxyHeaders {
		if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				req.RemoteAddr = first
			}
		} else if xrip := strings.TrimSpace(req.Header.Get("X-Real-IP")); xrip != "" {
			req.RemoteAddr = xrip
		}
	}

	w := &httpResponseWriter{
		conn:   conn,
		header: make(http.Header),
	}
	w.header.Set("Server", "PortRelay")
	w.header.Set("Connection", "close")

	m.handleHTTPRequest(w, req)
}

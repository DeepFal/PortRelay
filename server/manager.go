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
	"sort"
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
			m.unregisterTunnel(as, msg.TunnelID, msg.Deleted)
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
			m.revokeLease(scopeOf(msg.TunnelID, msg.GroupID), msg.IP)
		}

	case protocol.TypeCreateInvite:
		var msg protocol.CreateInviteMessage
		if json.Unmarshal(raw, &msg) == nil {
			as.send(m.createInvite(msg))
		}

	case protocol.TypeRevokeInvite:
		var msg protocol.RevokeInviteMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.revokeInvite(scopeOf(msg.TunnelID, msg.GroupID), msg.Code)
		}

	case protocol.TypeDecideRequest:
		var msg protocol.DecideRequestMessage
		if json.Unmarshal(raw, &msg) == nil {
			m.decideRequest(scopeOf(msg.TunnelID, msg.GroupID), msg.RequestID, msg.Approve)
		}

	case protocol.TypeExtendLease:
		var msg protocol.ExtendLeaseMessage
		if json.Unmarshal(raw, &msg) == nil {
			as.send(m.extendLease(msg))
		}

	case protocol.TypeDeleteGroup:
		var msg protocol.DeleteGroupMessage
		if json.Unmarshal(raw, &msg) == nil && msg.GroupID != "" {
			m.deleteGroup(msg.GroupID)
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
	m.tunnels[cfg.ID] = t
	m.portMap[cfg.ListenPort] = cfg.ID
	// 服务端重启或 Agent 重连后，按授权状态恢复该通道（及其所在组）尚未过期的临时放通
	m.syncAccess(t)
	if old != nil && old.GroupID() != cfg.GroupID {
		m.forgetGroupIfEmptyLocked(old.GroupID())
	}

	log.Printf("[Server] Registered tunnel '%s' listening on 0.0.0.0:%d", cfg.Name, cfg.ListenPort)
	return registerResp(cfg, nil)
}

func (m *Manager) unregisterTunnel(as *agentSession, tunnelID string, deleted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != as {
		return
	}
	// 主动关闭通道时，放通、邀请链接与待审批申请一并失效，已授权的设备 2 小时内仍可申请延长；
	// 删除通道则全部清除（断线重连不会走到这里）
	if deleted {
		m.state.dropScope(tunnelID)
	} else {
		m.state.closeScope(tunnelID)
	}
	if t, exists := m.tunnels[tunnelID]; exists {
		t.Close()
		delete(m.portMap, t.ListenPort())
		delete(m.tunnels, tunnelID)
		log.Printf("[Server] Unregistered tunnel '%s' (port %d freed)", t.Name(), t.ListenPort())
		// 组内最后一个开启的通道关闭后，组的邀请与授权一并失效（与单个通道关闭的语义一致）
		m.forgetGroupIfEmptyLocked(t.GroupID())
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

	prevGroup := t.GroupID()
	if t.ListenPort() == cfg.ListenPort {
		t.UpdateConfig(cfg)
		m.syncAccess(t)
		if prevGroup != cfg.GroupID {
			m.forgetGroupIfEmptyLocked(prevGroup)
		}
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
	m.syncAccess(nt)
	if prevGroup != cfg.GroupID {
		m.forgetGroupIfEmptyLocked(prevGroup)
	}
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

func (m *Manager) revokeLease(scope, ip string) {
	revoked := m.state.removeLease(scope, ip)
	kicked := m.syncScope(scope)
	target := ip
	if target == "" {
		target = "all"
	}
	log.Printf("[WebAuth] Revoked %d temporary lease(s) (%s) on %s, disconnected %d client(s)",
		revoked, target, m.scopeName(scope), kicked)
}

func (m *Manager) tunnel(id string) *Tunnel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tunnels[id]
}

// ---- 作用范围：单个通道或通道组 ----

// groupMembersLocked returns the open tunnels of a group ordered by port. Caller must hold m.mu.
func (m *Manager) groupMembersLocked(groupID string) []*Tunnel {
	if groupID == "" {
		return nil
	}
	var out []*Tunnel
	for _, t := range m.tunnels {
		if t.GroupID() == groupID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ListenPort() < out[j].ListenPort() })
	return out
}

// scopeTunnels returns the open tunnels an access scope applies to
func (m *Manager) scopeTunnels(scope string) []*Tunnel {
	id, isGroup := splitScope(scope)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if isGroup {
		return m.groupMembersLocked(id)
	}
	if t := m.tunnels[id]; t != nil {
		return []*Tunnel{t}
	}
	return nil
}

// scopeName is a display name for logs and pages
func (m *Manager) scopeName(scope string) string {
	tunnels := m.scopeTunnels(scope)
	if len(tunnels) == 0 {
		return scope
	}
	if _, isGroup := splitScope(scope); isGroup {
		if name := tunnels[0].Snapshot().GroupName; name != "" {
			return name
		}
		return "通道组"
	}
	return tunnels[0].Name()
}

// scopeExpiresOf caps leases by how long the scope stays open: a tunnel's own TTL, or for a
// group the TTL of its longest-lived member (nil when any member has no TTL)
func scopeExpiresOf(tunnels []*Tunnel) *time.Time {
	var latest *time.Time
	for _, t := range tunnels {
		exp := t.Snapshot().ExpiresAt
		if exp == nil {
			return nil
		}
		if latest == nil || exp.After(*latest) {
			v := *exp
			latest = &v
		}
	}
	return latest
}

// syncAccess makes a tunnel's temporary allowlist match the access state — its own leases plus
// those of its group — and disconnects clients that are no longer permitted.
func (m *Manager) syncAccess(t *Tunnel) int {
	m.state.setName(t.ID(), t.Name())
	leases := m.state.leaseExpiries(t.ID())
	if gid := t.GroupID(); gid != "" {
		if name := t.Snapshot().GroupName; name != "" {
			m.state.setName(groupScope(gid), name)
		}
		for ip, exp := range m.state.leaseExpiries(groupScope(gid)) {
			if cur, ok := leases[ip]; !ok || exp.After(cur) {
				leases[ip] = exp
			}
		}
	}
	return t.SetDynamic(leases)
}

// syncScope re-applies the access state to every open tunnel of a scope
func (m *Manager) syncScope(scope string) int {
	kicked := 0
	for _, t := range m.scopeTunnels(scope) {
		kicked += m.syncAccess(t)
	}
	return kicked
}

// forgetGroupIfEmptyLocked closes a group's shared access state once none of its tunnels is open
// (ended devices can still ask for an extension). Caller must hold m.mu.
func (m *Manager) forgetGroupIfEmptyLocked(groupID string) {
	if groupID == "" || len(m.groupMembersLocked(groupID)) > 0 {
		return
	}
	m.state.closeScope(groupScope(groupID))
}

// deleteGroup drops a dissolved group's invites, leases, requests and renewal records
func (m *Manager) deleteGroup(groupID string) {
	scope := groupScope(groupID)
	m.state.dropScope(scope)
	kicked := m.syncScope(scope)
	log.Printf("[WebAuth] Tunnel group %s dissolved, disconnected %d client(s)", groupID, kicked)
}

func (m *Manager) createInvite(msg protocol.CreateInviteMessage) protocol.InviteRespMessage {
	resp := protocol.InviteRespMessage{Type: protocol.TypeInviteResp, RequestID: msg.RequestID}
	scope := scopeOf(msg.TunnelID, msg.GroupID)
	open := len(m.scopeTunnels(scope)) > 0
	switch {
	case !open && msg.GroupID != "":
		resp.Error = "通道组内没有开启的通道，无法生成邀请链接"
	case !open:
		resp.Error = "通道未开启，无法生成邀请链接"
	case msg.ValidMinutes <= 0 || msg.ValidMinutes > 30*24*60:
		resp.Error = "邀请有效期须在 1 分钟到 30 天之间"
	case msg.MaxUses < 0:
		resp.Error = "可用设备数不能为负数"
	case !protocol.ValidLeaseDuration(msg.LeaseDuration):
		resp.Error = "无效的授权时长"
	default:
		resp.Invite = m.state.createInvite(scope, truncateRunes(msg.Note, 64), time.Duration(msg.ValidMinutes)*time.Minute, msg.MaxUses, msg.LeaseDuration, msg.RequireApproval)
		resp.Success = true
		log.Printf("[WebAuth] Created invite for %s (valid %dm, max %d device(s))", m.scopeName(scope), msg.ValidMinutes, msg.MaxUses)
	}
	return resp
}

func (m *Manager) revokeInvite(scope, code string) {
	ips := m.state.revokeInvite(scope, code)
	kicked := m.syncScope(scope)
	log.Printf("[WebAuth] Revoked invite on %s: %d lease(s) removed, %d client(s) disconnected", m.scopeName(scope), len(ips), kicked)
}

func (m *Manager) decideRequest(scope, requestID string, approve bool) {
	tunnels := m.scopeTunnels(scope)
	r, _, err := m.state.decide(scope, requestID, approve, scopeExpiresOf(tunnels))
	if err != nil {
		return
	}
	if approve {
		m.syncScope(scope)
	}
	log.Printf("[WebAuth] Request from %s (%s) on %s: %s", r.Name, r.IP, m.scopeName(scope), r.Status)
}

// extendLease extends a device's access (approving its renewal requests); the scope may be
// closed — the Agent reopens it, and registering re-applies the lease.
func (m *Manager) extendLease(msg protocol.ExtendLeaseMessage) protocol.ExtendLeaseRespMessage {
	resp := protocol.ExtendLeaseRespMessage{Type: protocol.TypeExtendResp, RequestID: msg.RequestID}
	scope := scopeOf(msg.TunnelID, msg.GroupID)
	if msg.Minutes < 1 || msg.Minutes > protocol.MaxLeaseMinutes || msg.IP == "" {
		resp.Error = "延长时长须在 1 分钟到 30 天之间"
		return resp
	}
	exp, err := m.state.extendLease(scope, msg.IP, msg.Minutes)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	m.syncScope(scope)
	resp.Success, resp.ExpiresAt = true, exp
	log.Printf("[WebAuth] Extended %s on %s by %s, until %s", msg.IP, m.scopeName(scope), protocol.DescribeMinutes(msg.Minutes), exp.Format("01-02 15:04"))
	return resp
}

// onClientConnect starts the countdown of a device's lease on its first connection.
// 组授权在首次连上组内任意一个通道时开始计时。
func (m *Manager) onClientConnect(tunnelID, ip string) {
	t := m.tunnel(tunnelID)
	if t == nil {
		return
	}
	now := time.Now()
	if exp, started := m.state.startLease(t.ID(), ip, t.Snapshot().ExpiresAt, now); started {
		m.syncAccess(t)
		log.Printf("[WebAuth] %s connected for the first time on tunnel %s, access valid until %s", ip, t.Name(), exp.Format("01-02 15:04"))
	}
	if gid := t.GroupID(); gid != "" {
		scope := groupScope(gid)
		if exp, started := m.state.startLease(scope, ip, scopeExpiresOf(m.scopeTunnels(scope)), now); started {
			m.syncScope(scope)
			log.Printf("[WebAuth] %s connected for the first time in group %s, access valid until %s", ip, m.scopeName(scope), exp.Format("01-02 15:04"))
		}
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
		groupIDs := make(map[string]bool)
		for _, t := range m.tunnels {
			if gid := t.GroupID(); gid != "" {
				groupIDs[gid] = true
			}
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
		groupMetrics := make([]protocol.GroupMetrics, 0, len(groupIDs))
		for gid := range groupIDs {
			scope := groupScope(gid)
			groupMetrics = append(groupMetrics, protocol.GroupMetrics{
				GroupID:       gid,
				DynamicLeases: m.state.leaseList(scope),
				Invites:       m.state.inviteList(scope),
				Pending:       m.state.pendingList(scope),
			})
		}
		m.mu.RUnlock()

		m.secEventsMu.Lock()
		secCopy := make([]protocol.SecurityEvent, len(m.secEvents))
		copy(secCopy, m.secEvents)
		m.secEventsMu.Unlock()

		as.send(protocol.MetricsMessage{
			Type:           protocol.TypeMetrics,
			Tunnels:        tunnelMetricsList,
			Groups:         groupMetrics,
			ActiveConns:    allActiveConns,
			SecurityEvents: secCopy,
			Renewals:       m.state.renewalList(),
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

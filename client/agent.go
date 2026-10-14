package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/yamux"

	"github.com/DeepFal/PortRelay/core/protocol"
)

const (
	authRespTimeout = 10 * time.Second
	reconnectDelay  = 2 * time.Second
)

// Agent runs on Mac and connects to the relay server
type Agent struct {
	cm *ConfigManager

	mu            sync.RWMutex
	session       *yamux.Session
	connected     bool
	lastError     string
	latestMetrics protocol.MetricsMessage
	regErrors     map[string]string // tunnelID -> 服务器返回的注册失败原因
	inviteWaiters map[string]chan protocol.InviteRespMessage
	extendWaiters map[string]chan protocol.ExtendLeaseRespMessage

	controlWriteMu sync.Mutex
	controlWriter  *bufio.Writer

	// 闲置自动关闭与实际连接时长统计（按通道）
	usageMu  sync.Mutex
	usage    map[string]*usageState
	idleUnit time.Duration // 闲置时长单位，正常为分钟（测试可调小）
	rates    *rateRecorder // 近 10 分钟流量曲线

	metricsSub      chan struct{}
	closed          chan struct{}
	supervisorDone  chan struct{}
	stopOnce        sync.Once
	reconnectSignal chan struct{}
}

// NewAgent initializes the agent
func NewAgent(cm *ConfigManager) *Agent {
	return &Agent{
		cm:              cm,
		regErrors:       make(map[string]string),
		inviteWaiters:   make(map[string]chan protocol.InviteRespMessage),
		extendWaiters:   make(map[string]chan protocol.ExtendLeaseRespMessage),
		usage:           make(map[string]*usageState),
		idleUnit:        time.Minute,
		rates:           newRateRecorder(),
		metricsSub:      make(chan struct{}, 1),
		closed:          make(chan struct{}),
		supervisorDone:  make(chan struct{}),
		reconnectSignal: make(chan struct{}, 1),
	}
}

// Reconnect drops the current session and reconnects immediately.
// force=false 时先探测现有会话，健康则保留（避免屏幕唤醒等场景无谓地切断所有连接）。
func (a *Agent) Reconnect(force bool) {
	a.mu.RLock()
	sess := a.session
	a.mu.RUnlock()

	if !force && sess != nil {
		if healthy(sess) {
			log.Printf("[Agent] 会话健康，无需重连")
			return
		}
	}

	log.Printf("[Agent] ⚡ 触发主动重连...")
	if sess != nil {
		_ = sess.Close()
	}
	select {
	case a.reconnectSignal <- struct{}{}:
	default:
	}
}

func healthy(sess *yamux.Session) bool {
	res := make(chan error, 1)
	go func() {
		_, err := sess.Ping()
		res <- err
	}()
	select {
	case err := <-res:
		return err == nil
	case <-time.After(3 * time.Second):
		return false
	}
}

// Start runs background connection supervisor and TTL checker
func (a *Agent) Start() {
	go a.supervisorLoop()
	go a.ttlCheckerLoop()
}

// Stop shuts down and waits (briefly) for the session to wind down, so the final
// traffic merge is in the config before the caller persists it. Safe to call more than once.
func (a *Agent) Stop() {
	a.stopOnce.Do(func() {
		close(a.closed)
		a.mu.Lock()
		if a.session != nil {
			_ = a.session.Close()
		}
		a.mu.Unlock()
		select {
		case <-a.supervisorDone:
		case <-time.After(3 * time.Second):
		}
	})
}

func (a *Agent) ttlCheckerLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.closed:
			return
		case <-ticker.C:
			for _, t := range a.cm.ExpireDue(a.ConsumeSessionBytes) {
				log.Printf("[Agent] ⏰ 通道「%s」(端口 %d) %s，已自动关闭", t.Name, t.ListenPort, t.CloseReason)
				a.dropUsage(t.ID)
				a.SyncTunnel(t, false)
			}
			a.trackUsage(time.Now())
		}
	}
}

// usageState tracks one enabled tunnel's activity during the current session
type usageState struct {
	lastActive  time.Time     // 最近一次“在用”的时间（闲置计时起点）
	lastTick    time.Time     // 上次统计时间
	inUse       bool          // 当前是否在用
	pendingConn time.Duration // 尚未落盘的实际连接时长
	firstMarked bool
}

// TunnelActivity is the live usage view of an enabled tunnel
type TunnelActivity struct {
	InUse        bool       `json:"in_use"`
	IdleSince    *time.Time `json:"idle_since,omitempty"`
	AutoCloseAt  *time.Time `json:"auto_close_at,omitempty"`
	ConnectedSec int64      `json:"connected_sec"` // 本次开启后有连接在线的累计秒数
}

// inUse: 有在线连接、有效授权（含尚未首次连接的）、待审批申请或仍可使用的邀请
func tunnelInUse(m protocol.TunnelMetrics) bool {
	return m.ActiveConns > 0 || accessInUse(m.DynamicLeases, m.Pending, m.Invites)
}

func accessInUse(leases []protocol.DynamicLease, pending []protocol.AccessRequest, invites []protocol.Invite) bool {
	if len(leases) > 0 || len(pending) > 0 {
		return true
	}
	for _, inv := range invites {
		if inv.MaxUses <= 0 || len(inv.Uses) < inv.MaxUses {
			return true
		}
	}
	return false
}

const connectedFlushInterval = 10 * time.Second

// trackUsage runs every second: updates idle timers and real connected time, and closes idle tunnels
func (a *Agent) trackUsage(now time.Time) {
	cfg := a.cm.Get()
	metrics := a.GetLatestMetrics()
	connected := a.IsConnected()
	byID := make(map[string]protocol.TunnelMetrics, len(metrics.Tunnels))
	for _, m := range metrics.Tunnels {
		byID[m.TunnelID] = m
	}
	// 通道组整体在用：组内任一通道在用，或组的邀请 / 授权 / 申请仍有效。组内通道一起计闲置、一起关闭。
	groupBusy := make(map[string]bool)
	for _, gm := range metrics.Groups {
		if accessInUse(gm.DynamicLeases, gm.Pending, gm.Invites) {
			groupBusy[gm.GroupID] = true
		}
	}
	// 等待批准的延长申请：通道（组）保持开启，避免关了又被批准重开
	renewing := make(map[string]bool)
	for _, r := range metrics.Renewals {
		if r.Request == nil {
			continue
		}
		if r.GroupID != "" {
			groupBusy[r.GroupID] = true
		} else {
			renewing[r.TunnelID] = true
		}
	}
	for _, t := range cfg.Tunnels {
		if m, ok := byID[t.ID]; ok && t.Enabled && t.GroupID != "" && m.Status == "online" && tunnelInUse(m) {
			groupBusy[t.GroupID] = true
		}
	}

	type closing struct {
		id, reason string
		flush      int64
	}
	var toClose []closing
	var firstConn []string
	flush := map[string]int64{}

	a.usageMu.Lock()
	enabled := map[string]bool{}
	for _, t := range cfg.Tunnels {
		if !t.Enabled {
			continue
		}
		enabled[t.ID] = true
		u := a.usage[t.ID]
		if u == nil {
			u = &usageState{lastActive: now, lastTick: now, firstMarked: t.FirstConnectedAt != nil}
			a.usage[t.ID] = u
		}
		dt := now.Sub(u.lastTick)
		u.lastTick = now

		m, ok := byID[t.ID]
		if !connected || !ok || m.Status != "online" {
			// 未连接服务器或尚未收到指标：暂停闲置计时，避免把网络中断误判为闲置
			u.lastActive = now
			u.inUse = false
			continue
		}
		if m.ActiveConns > 0 {
			u.pendingConn += dt
			if !u.firstMarked {
				u.firstMarked = true
				firstConn = append(firstConn, t.ID)
			}
		}
		u.inUse = tunnelInUse(m) || renewing[t.ID] || (t.GroupID != "" && groupBusy[t.GroupID])
		if u.inUse {
			u.lastActive = now
		}
		// 即将因最长开启时长关闭时先落盘，避免丢失最后一段连接时长
		expiring := t.ExpiresAt != nil && !now.Before(t.ExpiresAt.Add(-time.Second))
		if u.pendingConn >= connectedFlushInterval || (expiring && u.pendingConn >= time.Second) {
			secs := int64(u.pendingConn / time.Second)
			u.pendingConn -= time.Duration(secs) * time.Second
			flush[t.ID] = secs
		}
		if idle := IdleCloseMinutes(t); idle > 0 && now.Sub(u.lastActive) >= time.Duration(idle)*a.idleUnit {
			secs := int64(u.pendingConn / time.Second)
			toClose = append(toClose, closing{t.ID, fmt.Sprintf("闲置 %s 自动关闭", formatMinutes(idle)), secs})
			delete(a.usage, t.ID)
		}
	}
	for id := range a.usage {
		if !enabled[id] {
			delete(a.usage, id)
		}
	}
	a.usageMu.Unlock()
	a.rates.retain(enabled)

	for _, id := range firstConn {
		a.cm.MarkFirstConnected(id, now)
	}
	for id, secs := range flush {
		a.cm.AddConnectedSeconds(id, secs)
	}
	for _, c := range toClose {
		a.cm.AddConnectedSeconds(c.id, c.flush)
		if t, ok := a.cm.CloseTunnel(c.id, c.reason, a.ConsumeSessionBytes); ok {
			log.Printf("[Agent] 💤 通道「%s」(端口 %d) %s", t.Name, t.ListenPort, c.reason)
			a.SyncTunnel(t, false)
		}
	}
}

// FlushUsage persists the not-yet-saved connected time of a tunnel (before it is stopped)
func (a *Agent) FlushUsage(id string) {
	a.usageMu.Lock()
	var secs int64
	if u := a.usage[id]; u != nil {
		secs = int64(u.pendingConn / time.Second)
		u.pendingConn -= time.Duration(secs) * time.Second
	}
	a.usageMu.Unlock()
	a.cm.AddConnectedSeconds(id, secs)
}

func (a *Agent) dropUsage(id string) {
	a.usageMu.Lock()
	delete(a.usage, id)
	a.usageMu.Unlock()
}

// RateSeries returns the last 10 minutes of a tunnel's rates (id "" = all tunnels)
func (a *Agent) RateSeries(id string) RateSeries {
	if id == "" {
		id = totalSeries
	}
	return a.rates.series(id, time.Now())
}

// GroupRateSeries sums the rate curves of several tunnels (a tunnel group)
func (a *Agent) GroupRateSeries(ids []string) RateSeries {
	now := time.Now()
	sum := (&rateHistory{}).series(now)
	for _, id := range ids {
		s := a.rates.series(id, now)
		for k := range sum.Up {
			sum.Up[k] += s.Up[k]
			sum.Down[k] += s.Down[k]
		}
	}
	return sum
}

// Activity returns the live usage of an enabled tunnel (nil when not tracked yet)
func (a *Agent) Activity(t protocol.TunnelConfig) *TunnelActivity {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	u := a.usage[t.ID]
	if u == nil || !t.Enabled {
		return nil
	}
	act := &TunnelActivity{InUse: u.inUse, ConnectedSec: t.ConnectedSec + int64(u.pendingConn/time.Second)}
	if !u.inUse {
		since := u.lastActive
		act.IdleSince = &since
		if idle := IdleCloseMinutes(t); idle > 0 {
			at := since.Add(time.Duration(idle) * a.idleUnit)
			act.AutoCloseAt = &at
		}
	}
	return act
}

func (a *Agent) supervisorLoop() {
	defer close(a.supervisorDone)
	for {
		select {
		case <-a.closed:
			return
		default:
		}

		cfg := a.cm.Get()
		if cfg.ServerAddr == "" {
			// 首次运行尚未配置服务器：等用户在设置中填写后再连接
			a.mu.Lock()
			a.lastError = "尚未配置服务器地址，请在设置中填写"
			a.mu.Unlock()
			select {
			case <-a.closed:
				return
			case <-a.reconnectSignal:
			case <-time.After(reconnectDelay):
			}
			continue
		}
		log.Printf("[Agent] 正在连接中继服务器 (%s)...", cfg.ServerAddr)

		err := a.connectAndRun(cfg)
		if err != nil {
			log.Printf("[Agent] 连接中断或失败: %v, %v 后尝试重连...", err, reconnectDelay)
		}
		a.resetSessionState(err)

		select {
		case <-a.closed:
			return
		case <-a.reconnectSignal:
			log.Printf("[Agent] 立即执行重连...")
		case <-time.After(reconnectDelay):
		}
	}
}

// resetSessionState 会话结束：把本次会话流量并入历史（服务端会重置计数），清掉过期指标
func (a *Agent) resetSessionState(err error) {
	a.controlWriteMu.Lock()
	a.controlWriter = nil
	a.controlWriteMu.Unlock()

	// 锁顺序固定为 cm.mu -> a.mu（ExpireDue 持 cm.mu 回调 ConsumeSessionBytes），这里先读配置再加锁
	enabled := make(map[string]bool)
	for _, t := range a.cm.Get().Tunnels {
		enabled[t.ID] = t.Enabled
	}
	a.mu.Lock()
	carry := make(map[string][2]int64)
	for _, m := range a.latestMetrics.Tunnels {
		if enabled[m.TunnelID] {
			carry[m.TunnelID] = [2]int64{m.BytesIn, m.BytesOut}
		}
	}
	a.connected = false
	a.session = nil
	a.latestMetrics = protocol.MetricsMessage{}
	a.regErrors = make(map[string]string)
	if err != nil && !errors.Is(err, io.EOF) {
		a.lastError = err.Error()
	} else {
		a.lastError = "与中继服务器的连接已断开"
	}
	a.mu.Unlock()

	a.cm.AddHistoricalTraffic(carry)
}

func (a *Agent) connectAndRun(cfg Config) error {
	rawConn, err := net.DialTimeout("tcp", cfg.ServerAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("dial server failed: %w", err)
	}
	defer rawConn.Close()

	yCfg := yamux.DefaultConfig()
	yCfg.EnableKeepAlive = true
	yCfg.KeepAliveInterval = 10 * time.Second
	yCfg.LogOutput = io.Discard

	session, err := yamux.Client(rawConn, yCfg)
	if err != nil {
		return fmt.Errorf("yamux client init error: %w", err)
	}
	defer session.Close()

	a.mu.Lock()
	a.session = session
	a.mu.Unlock()

	controlStream, err := session.OpenStream()
	if err != nil {
		return fmt.Errorf("open control stream failed: %w", err)
	}
	defer controlStream.Close()

	writer := bufio.NewWriter(controlStream)
	reader := bufio.NewReader(controlStream)

	// 1. Authenticate and wait for the verdict before declaring the link usable
	if err := writeJSONLine(writer, protocol.AuthMessage{Type: protocol.TypeAuth, Token: cfg.Token}); err != nil {
		return err
	}
	_ = controlStream.SetReadDeadline(time.Now().Add(authRespTimeout))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("等待鉴权结果失败: %w", err)
	}
	_ = controlStream.SetReadDeadline(time.Time{})
	var authResp protocol.AuthRespMessage
	if err := json.Unmarshal(line, &authResp); err != nil || authResp.Type != protocol.TypeAuthResp {
		return fmt.Errorf("服务端返回了非预期的鉴权响应")
	}
	if !authResp.Success {
		return fmt.Errorf("身份鉴权失败: %s", authResp.Error)
	}

	a.controlWriteMu.Lock()
	a.controlWriter = writer
	a.controlWriteMu.Unlock()

	a.mu.Lock()
	a.connected = true
	a.lastError = ""
	a.mu.Unlock()
	log.Printf("[Agent] 成功与中继服务器握手并通过鉴权！")

	// 2. Register all enabled tunnels
	for _, t := range a.cm.Get().Tunnels {
		if t.Enabled {
			log.Printf("[Agent] 注册通道: %s (:%d -> %s) [责任人: %s]", t.Name, t.ListenPort, t.Target, t.Owner)
			_ = a.sendControl(protocol.RegisterTunnelMessage{Type: protocol.TypeRegisterTunnel, Tunnel: a.wire(t)})
		}
	}

	// 3. Start data stream acceptor loop
	go a.acceptDataStreams(session)

	// 4. Read incoming control messages
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("[Agent] control stream read error: %v", err)
			}
			return err
		}
		if len(line) == 0 {
			continue
		}

		var base protocol.BaseMessage
		if err := json.Unmarshal(line, &base); err != nil {
			continue
		}

		switch base.Type {
		case protocol.TypeMetrics:
			var msg protocol.MetricsMessage
			if err := json.Unmarshal(line, &msg); err == nil {
				a.storeMetrics(msg)
			}
		case protocol.TypeInviteResp:
			var resp protocol.InviteRespMessage
			if json.Unmarshal(line, &resp) != nil {
				continue
			}
			a.mu.Lock()
			if ch, ok := a.inviteWaiters[resp.RequestID]; ok {
				ch <- resp
				delete(a.inviteWaiters, resp.RequestID)
			}
			a.mu.Unlock()
		case protocol.TypeExtendResp:
			var resp protocol.ExtendLeaseRespMessage
			if json.Unmarshal(line, &resp) != nil {
				continue
			}
			a.mu.Lock()
			if ch, ok := a.extendWaiters[resp.RequestID]; ok {
				ch <- resp
				delete(a.extendWaiters, resp.RequestID)
			}
			a.mu.Unlock()
		case protocol.TypeRegisterResp:
			var resp protocol.RegisterRespMessage
			if json.Unmarshal(line, &resp) != nil {
				continue
			}
			a.mu.Lock()
			if resp.Success {
				delete(a.regErrors, resp.TunnelID)
			} else {
				a.regErrors[resp.TunnelID] = resp.Error
				log.Printf("[Agent] 通道 %s 注册失败: %s", resp.TunnelID, resp.Error)
			}
			a.mu.Unlock()
		}
	}
}

// storeMetrics 只保留当前处于开启状态的通道，避免已关闭通道的旧指标被重复计入历史流量
func (a *Agent) storeMetrics(msg protocol.MetricsMessage) {
	enabled := make(map[string]bool)
	for _, t := range a.cm.Get().Tunnels {
		if t.Enabled {
			enabled[t.ID] = true
		}
	}
	filtered := msg.Tunnels[:0]
	for _, m := range msg.Tunnels {
		if enabled[m.TunnelID] {
			filtered = append(filtered, m)
		}
	}
	msg.Tunnels = filtered

	rates := make(map[string][2]int64, len(msg.Tunnels))
	for _, m := range msg.Tunnels {
		rates[m.TunnelID] = [2]int64{m.RateIn, m.RateOut}
	}
	a.rates.record(time.Now(), rates)

	a.mu.Lock()
	a.latestMetrics = msg
	a.mu.Unlock()

	select {
	case a.metricsSub <- struct{}{}:
	default:
	}
}

// ConsumeSessionBytes returns the current session traffic of a tunnel and drops it from the
// cached metrics so it cannot be merged into history twice.
func (a *Agent) ConsumeSessionBytes(id string) (int64, int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	tunnels := a.latestMetrics.Tunnels
	for i, m := range tunnels {
		if m.TunnelID == id {
			out := make([]protocol.TunnelMetrics, 0, len(tunnels)-1)
			out = append(out, tunnels[:i]...)
			out = append(out, tunnels[i+1:]...)
			a.latestMetrics.Tunnels = out
			return m.BytesIn, m.BytesOut
		}
	}
	return 0, 0
}

func (a *Agent) acceptDataStreams(session *yamux.Session) {
	for {
		stream, err := session.AcceptStream()
		if err != nil {
			return
		}
		go a.handleDataStream(stream)
	}
}

func (a *Agent) handleDataStream(stream net.Conn) {
	defer stream.Close()

	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(stream)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	var hs protocol.StreamHandshake
	if err := json.Unmarshal(line, &hs); err != nil {
		return
	}

	t, ok := a.cm.FindTunnel(hs.TunnelID)
	if !ok || !t.Enabled {
		return
	}

	connStartTime := time.Now()

	targetConn, err := net.DialTimeout("tcp", t.Target, 3*time.Second)
	if err != nil {
		log.Printf("[Agent] 目标主机 %s 无法连接 (客户端 %s): %v", t.Target, hs.ClientIP, err)
		return
	}
	defer targetConn.Close()

	// Count bytes for connection history
	var bytesIn, bytesOut atomic.Int64
	done := make(chan struct{}, 2)

	go func() {
		buf := make([]byte, 32*1024)
		for {
			nr, er := reader.Read(buf)
			if nr > 0 {
				bytesIn.Add(int64(nr))
				nw, ew := targetConn.Write(buf[:nr])
				if ew != nil || nr != nw {
					break
				}
			}
			if er != nil {
				break
			}
		}
		_ = targetConn.Close()
		_ = stream.Close()
		done <- struct{}{}
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			nr, er := targetConn.Read(buf)
			if nr > 0 {
				bytesOut.Add(int64(nr))
				nw, ew := stream.Write(buf[:nr])
				if ew != nil || nr != nw {
					break
				}
			}
			if er != nil {
				break
			}
		}
		_ = targetConn.Close()
		_ = stream.Close()
		done <- struct{}{}
	}()

	<-done
	<-done

	// Record client connection history
	connEndTime := time.Now()
	a.cm.AddClientHistory(protocol.ClientHistoryRecord{
		ID:             uuid.New().String(),
		TunnelID:       hs.TunnelID,
		TunnelName:     t.Name,
		Port:           t.ListenPort,
		ClientIP:       hs.ClientIP,
		ConnectedAt:    connStartTime,
		DisconnectedAt: connEndTime,
		DurationSec:    int64(connEndTime.Sub(connStartTime).Seconds()),
		BytesIn:        bytesIn.Load(),
		BytesOut:       bytesOut.Load(),
	})
}

func writeJSONLine(w *bufio.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Flush()
}

func (a *Agent) sendControl(v any) error {
	a.controlWriteMu.Lock()
	defer a.controlWriteMu.Unlock()

	if a.controlWriter == nil {
		return fmt.Errorf("control stream not connected")
	}
	return writeJSONLine(a.controlWriter, v)
}

// SyncTunnel pushes a tunnel's current state to the relay server.
// edited=true 时使用 update_tunnel（端口不变则原地更新，不断开现有连接）。
func (a *Agent) SyncTunnel(t protocol.TunnelConfig, edited bool) {
	if !t.Enabled {
		a.mu.Lock()
		delete(a.regErrors, t.ID)
		a.mu.Unlock()
		a.RemoveTunnel(t.ID)
		return
	}
	if !a.IsConnected() {
		return
	}
	if edited {
		_ = a.sendControl(protocol.UpdateTunnelMessage{Type: protocol.TypeUpdateTunnel, Tunnel: a.wire(t)})
	} else {
		_ = a.sendControl(protocol.RegisterTunnelMessage{Type: protocol.TypeRegisterTunnel, Tunnel: a.wire(t)})
	}
}

// wire fills in what the relay server needs besides the stored config (the group's display name)
func (a *Agent) wire(t protocol.TunnelConfig) protocol.TunnelConfig {
	t.GroupName = ""
	if t.GroupID != "" {
		if g, ok := a.cm.FindGroup(t.GroupID); ok {
			t.GroupName = g.Name
		}
	}
	return t
}

// RemoveTunnel closes a tunnel on the relay server; its devices can still ask for an extension
func (a *Agent) RemoveTunnel(id string) {
	a.unregister(id, false)
}

// DeleteTunnel removes a deleted tunnel from the relay server, renewal records included
func (a *Agent) DeleteTunnel(id string) {
	a.unregister(id, true)
}

func (a *Agent) unregister(id string, deleted bool) {
	a.mu.Lock()
	delete(a.regErrors, id)
	a.mu.Unlock()
	if !a.IsConnected() {
		return
	}
	_ = a.sendControl(protocol.UnregisterMessage{Type: protocol.TypeUnregister, TunnelID: id, Deleted: deleted})
}

// Scope selects what an invite, request or lease applies to: one tunnel, or a whole group
type Scope struct {
	TunnelID string
	GroupID  string
}

// CreateInvite asks the relay server for a new invite link and waits for the result
func (a *Agent) CreateInvite(scope Scope, note string, validMinutes, maxUses int, leaseDuration string, requireApproval bool) (protocol.Invite, error) {
	if !a.IsConnected() {
		return protocol.Invite{}, fmt.Errorf("未连接中继服务器")
	}
	reqID := uuid.New().String()
	ch := make(chan protocol.InviteRespMessage, 1)
	a.mu.Lock()
	a.inviteWaiters[reqID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.inviteWaiters, reqID)
		a.mu.Unlock()
	}()

	if err := a.sendControl(protocol.CreateInviteMessage{
		Type: protocol.TypeCreateInvite, RequestID: reqID, TunnelID: scope.TunnelID, GroupID: scope.GroupID,
		Note: note, ValidMinutes: validMinutes, MaxUses: maxUses, LeaseDuration: leaseDuration, RequireApproval: requireApproval,
	}); err != nil {
		return protocol.Invite{}, err
	}
	select {
	case resp := <-ch:
		if !resp.Success {
			return protocol.Invite{}, fmt.Errorf("%s", resp.Error)
		}
		return resp.Invite, nil
	case <-time.After(5 * time.Second):
		return protocol.Invite{}, fmt.Errorf("服务器未响应，请检查是否已升级到支持邀请链接的版本")
	}
}

// ExtendLease extends a device's access by minutes (approving its renewal requests) and returns
// the new expiry
func (a *Agent) ExtendLease(scope Scope, ip string, minutes int) (time.Time, error) {
	if !a.IsConnected() {
		return time.Time{}, fmt.Errorf("未连接中继服务器")
	}
	reqID := uuid.New().String()
	ch := make(chan protocol.ExtendLeaseRespMessage, 1)
	a.mu.Lock()
	a.extendWaiters[reqID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.extendWaiters, reqID)
		a.mu.Unlock()
	}()
	if err := a.sendControl(protocol.ExtendLeaseMessage{
		Type: protocol.TypeExtendLease, RequestID: reqID, TunnelID: scope.TunnelID, GroupID: scope.GroupID, IP: ip, Minutes: minutes,
	}); err != nil {
		return time.Time{}, err
	}
	select {
	case resp := <-ch:
		if !resp.Success {
			return time.Time{}, fmt.Errorf("%s", resp.Error)
		}
		return resp.ExpiresAt, nil
	case <-time.After(5 * time.Second):
		return time.Time{}, fmt.Errorf("服务器未响应，请先把服务端更新到支持延长授权的版本")
	}
}

// RevokeInvite invalidates an invite link and the leases it granted
func (a *Agent) RevokeInvite(scope Scope, code string) error {
	if !a.IsConnected() {
		return fmt.Errorf("未连接中继服务器")
	}
	return a.sendControl(protocol.RevokeInviteMessage{Type: protocol.TypeRevokeInvite, TunnelID: scope.TunnelID, GroupID: scope.GroupID, Code: code})
}

// DecideRequest approves or rejects a pending access request
func (a *Agent) DecideRequest(scope Scope, requestID string, approve bool) error {
	if !a.IsConnected() {
		return fmt.Errorf("未连接中继服务器")
	}
	return a.sendControl(protocol.DecideRequestMessage{Type: protocol.TypeDecideRequest, TunnelID: scope.TunnelID, GroupID: scope.GroupID, RequestID: requestID, Approve: approve})
}

// RevokeLease removes a temporary whitelist lease on the relay server (all leases when ip is empty)
func (a *Agent) RevokeLease(scope Scope, ip string) error {
	if !a.IsConnected() {
		return fmt.Errorf("未连接中继服务器")
	}
	return a.sendControl(protocol.RevokeLeaseMessage{Type: protocol.TypeRevokeLease, TunnelID: scope.TunnelID, GroupID: scope.GroupID, IP: ip})
}

// DeleteGroup tells the relay server to drop a dissolved group's invites and leases
func (a *Agent) DeleteGroup(groupID string) {
	if !a.IsConnected() {
		return
	}
	_ = a.sendControl(protocol.DeleteGroupMessage{Type: protocol.TypeDeleteGroup, GroupID: groupID})
}

func (a *Agent) KillConn(connID string) {
	if !a.IsConnected() {
		return
	}
	_ = a.sendControl(protocol.KillConnMessage{Type: protocol.TypeKillConn, ConnID: connID})
}

func (a *Agent) IsConnected() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.connected
}

// LastError returns why the agent is currently not connected (empty when connected)
func (a *Agent) LastError() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastError
}

// TunnelError returns the last registration error reported by the relay server for a tunnel
func (a *Agent) TunnelError(id string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.regErrors[id]
}

func (a *Agent) GetLatestMetrics() protocol.MetricsMessage {
	a.mu.RLock()
	defer a.mu.RUnlock()
	m := a.latestMetrics
	m.Tunnels = append([]protocol.TunnelMetrics(nil), m.Tunnels...)
	m.Groups = append([]protocol.GroupMetrics(nil), m.Groups...)
	m.ActiveConns = append([]protocol.ActiveConnInfo(nil), m.ActiveConns...)
	m.SecurityEvents = append([]protocol.SecurityEvent(nil), m.SecurityEvents...)
	m.Renewals = append([]protocol.RenewalInfo(nil), m.Renewals...)
	return m
}

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DeepFal/PortRelay/core/protocol"
)

const maxBodyBytes = 64 * 1024

// APIServer provides REST and SSE endpoints for the dashboard
type APIServer struct {
	cm    *ConfigManager
	agent *Agent
	fs    http.FileSystem

	// OnShutdown 由 POST /api/shutdown 触发（菜单栏“退出”时彻底结束后台引擎）
	OnShutdown func()
}

// NewAPIServer creates an API server
func NewAPIServer(cm *ConfigManager, agent *Agent, fs http.FileSystem) *APIServer {
	return &APIServer{
		cm:    cm,
		agent: agent,
		fs:    fs,
	}
}

// Handler returns the http handler with routing
func (s *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/tunnels", s.handleTunnels)
	mux.HandleFunc("/api/tunnels/", s.handleTunnelItem)
	mux.HandleFunc("/api/groups", s.handleGroups)
	mux.HandleFunc("/api/groups/", s.handleGroupItem)
	mux.HandleFunc("/api/probe", s.handleProbe)
	mux.HandleFunc("/api/recommend-port", s.handleRecommendPort)
	mux.HandleFunc("/api/connections", s.handleConnections)
	mux.HandleFunc("/api/connections/kill", s.handleKillConn)
	mux.HandleFunc("/api/history", s.handleClientHistory)
	mux.HandleFunc("/api/reconnect", s.handleReconnect)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/stream", s.handleSSE)
	mux.HandleFunc("/api/shutdown", s.handleShutdown)

	// Static web files
	if s.fs != nil {
		fileServer := http.FileServer(s.fs)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api") {
				http.NotFound(w, r)
				return
			}
			fileServer.ServeHTTP(w, r)
		})
	}

	return localOnlyMiddleware(mux)
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// localOnlyMiddleware 只接受本机面板与原生 App 的请求：
//   - Host 必须是回环地址（防 DNS rebinding）
//   - 带 Origin 的请求必须同源（防任意网页通过浏览器跨站调用本地 API）
func localOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *APIServer) statusPayload() map[string]any {
	cfg := s.cm.Get()
	return map[string]any{
		"connected":          s.agent.IsConnected(),
		"server_addr":        cfg.ServerAddr,
		"public_host":        cfg.PublicHostOrDefault(),
		"last_error":         s.agent.LastError(),
		"invite_base":        s.inviteBase(cfg),
		"total_rate_history": s.agent.RateSeries(""),
		"port_range":         fmt.Sprintf("%d - %d", PortMin, PortMax),
		"timestamp":          time.Now(),
	}
}

func (s *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, s.statusPayload())
}

// TunnelWithMetrics joins config with current metrics
type TunnelWithMetrics struct {
	protocol.TunnelConfig
	Metrics  protocol.TunnelMetrics `json:"metrics"`
	Activity *TunnelActivity        `json:"activity,omitempty"`
	// EffectiveIdleCloseMinutes 生效的闲置自动关闭时长（0 表示不自动关闭）
	EffectiveIdleCloseMinutes int `json:"effective_idle_close_minutes"`
	// RateHistory 近 10 分钟流量曲线（仅开启中的通道）
	RateHistory *RateSeries `json:"rate_history,omitempty"`
	// Renewals 可延长的设备与延长申请（通道关闭后 2 小时内仍有）
	Renewals []protocol.RenewalInfo `json:"renewals,omitempty"`
}

// renewalsOf picks the renewals of one tunnel (groupID empty) or one group
func renewalsOf(all []protocol.RenewalInfo, tunnelID, groupID string) []protocol.RenewalInfo {
	var out []protocol.RenewalInfo
	for _, r := range all {
		if (groupID != "" && r.GroupID == groupID) || (groupID == "" && r.GroupID == "" && r.TunnelID == tunnelID) {
			out = append(out, r)
		}
	}
	return out
}

// tunnelsWithMetrics merges config, live metrics and registration errors into one consistent view.
// status: disabled | offline (未连接服务器) | error (服务器拒绝注册) | syncing (等待指标) | online
func (s *APIServer) tunnelsWithMetrics(metrics protocol.MetricsMessage) []TunnelWithMetrics {
	cfg := s.cm.Get()
	connected := s.agent.IsConnected()
	metricsMap := make(map[string]protocol.TunnelMetrics)
	for _, m := range metrics.Tunnels {
		metricsMap[m.TunnelID] = m
	}

	result := make([]TunnelWithMetrics, 0, len(cfg.Tunnels))
	for _, t := range cfg.Tunnels {
		tm := protocol.TunnelMetrics{TunnelID: t.ID, ListenPort: t.ListenPort}
		switch {
		case !t.Enabled:
			tm.Status = "disabled"
		case !connected:
			tm.Status = "offline"
		case s.agent.TunnelError(t.ID) != "":
			tm.Status = "error"
			tm.Error = s.agent.TunnelError(t.ID)
		default:
			if m, ok := metricsMap[t.ID]; ok {
				tm = m
			} else {
				tm.Status = "syncing"
			}
		}
		item := TunnelWithMetrics{
			TunnelConfig:              t,
			Metrics:                   tm,
			Activity:                  s.agent.Activity(t),
			EffectiveIdleCloseMinutes: IdleCloseMinutes(t),
			Renewals:                  renewalsOf(metrics.Renewals, t.ID, ""),
		}
		if t.Enabled {
			series := s.agent.RateSeries(t.ID)
			item.RateHistory = &series
		}
		result = append(result, item)
	}
	return result
}

func (s *APIServer) handleTunnels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, http.StatusOK, s.tunnelsWithMetrics(s.agent.GetLatestMetrics()))

	case http.MethodPost:
		var t protocol.TunnelConfig
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON payload")
			return
		}
		created, err := s.cm.AddTunnel(t)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.agent.SyncTunnel(created, false)
		jsonResponse(w, http.StatusCreated, created)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *APIServer) handleTunnelItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[2] == "" {
		jsonError(w, http.StatusBadRequest, "Missing tunnel ID")
		return
	}
	tunnelID := parts[2]

	if tunnelID == "batch" && len(parts) == 3 && r.Method == http.MethodPost {
		s.handleTunnelBatch(w, r)
		return
	}
	if len(parts) == 4 && parts[3] == "toggle" && r.Method == http.MethodPost {
		s.handleToggle(w, r, tunnelID)
		return
	}

	// 授权相关：leases/revoke、invites、requests（与通道组共用同一套处理）
	if len(parts) >= 4 && isAccessPath(parts[3]) {
		if _, ok := s.cm.FindTunnel(tunnelID); !ok {
			jsonError(w, http.StatusNotFound, "通道未找到")
			return
		}
		s.handleAccess(w, r, Scope{TunnelID: tunnelID}, parts[3:])
		return
	}

	if len(parts) == 4 && parts[3] == "refresh-auth-code" && r.Method == http.MethodPost {
		t, err := s.cm.RefreshAuthCode(tunnelID)
		if err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		s.agent.SyncTunnel(t, true)
		jsonResponse(w, http.StatusOK, t)
		return
	}

	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid body")
			return
		}
		updated, _, err := s.cm.PatchTunnel(tunnelID, body)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.agent.SyncTunnel(updated, true)
		jsonResponse(w, http.StatusOK, updated)

	case http.MethodDelete:
		if err := s.cm.DeleteTunnel(tunnelID); err != nil {
			jsonError(w, http.StatusNotFound, err.Error())
			return
		}
		s.agent.ConsumeSessionBytes(tunnelID)
		s.agent.DeleteTunnel(tunnelID)
		jsonResponse(w, http.StatusOK, map[string]string{"status": "deleted"})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleToggle 支持 {"enabled": true|false} 指定目标状态（幂等，防止重复点击来回翻转）；不带 body 时取反
func (s *APIServer) handleToggle(w http.ResponseWriter, r *http.Request, tunnelID string) {
	want, err := decodeToggle(r)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	t, err := s.setEnabled(tunnelID, want)
	if err != nil {
		jsonError(w, http.StatusNotFound, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, t)
}

// decodeToggle reads {"enabled": bool}; nil means "flip"
func decodeToggle(r *http.Request) (*bool, error) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			return nil, err
		}
	}
	return req.Enabled, nil
}

// setEnabled turns a tunnel on or off (want nil flips it), merging session traffic and syncing the server
func (s *APIServer) setEnabled(tunnelID string, want *bool) (protocol.TunnelConfig, error) {
	current, err := s.cm.CurrentEnabled(tunnelID)
	if err != nil {
		return protocol.TunnelConfig{}, err
	}
	target := !current
	if want != nil {
		target = *want
	}
	var sessIn, sessOut int64
	if !target && current {
		s.agent.FlushUsage(tunnelID)
	}
	if target != current {
		// 关闭时把本次会话流量并入历史；开启时清掉可能残留的旧指标
		sessIn, sessOut = s.agent.ConsumeSessionBytes(tunnelID)
	}
	t, changed, err := s.cm.SetTunnelEnabled(tunnelID, target, sessIn, sessOut)
	if err != nil {
		return t, err
	}
	if changed {
		s.agent.dropUsage(tunnelID)
		s.agent.SyncTunnel(t, false)
	}
	return t, nil
}

// inviteBase is the URL prefix of invite links: http://<对外访问地址>:<控制端口>/i/
func (s *APIServer) inviteBase(cfg Config) string {
	port := "33000"
	if _, p, err := net.SplitHostPort(cfg.ServerAddr); err == nil {
		port = p
	}
	host := cfg.PublicHostOrDefault()
	if host == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(host, port) + "/i/"
}

// shareText is the ready-to-send message (e.g. for WeChat) for an invite
func shareText(cfg Config, t protocol.TunnelConfig, inv protocol.Invite, link string) string {
	return shareMessage(inv, fmt.Sprintf("通道：%s\n访问地址：%s:%d", t.Name, cfg.PublicHostOrDefault(), t.ListenPort), link)
}

// groupShareText lists every open address of a group with the target behind it
func groupShareText(cfg Config, g TunnelGroup, members []protocol.TunnelConfig, inv protocol.Invite, link string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "通道组：%s（%d 个地址）\n访问地址：", g.Name, len(members))
	for _, t := range members {
		fmt.Fprintf(&b, "\n  %s:%d → %s", cfg.PublicHostOrDefault(), t.ListenPort, t.Target)
	}
	return shareMessage(inv, b.String(), link)
}

func shareMessage(inv protocol.Invite, body, link string) string {
	var b strings.Builder
	b.WriteString("【PortRelay 访问邀请】")
	if inv.Note != "" {
		b.WriteString("（给 " + inv.Note + "）")
	}
	b.WriteString("\n" + body)
	action := "填写姓名后即可访问"
	if inv.RequireApproval {
		action = "填写姓名提交申请，管理员批准后即可访问"
	}
	b.WriteString(fmt.Sprintf("\n请在 %s 前用浏览器打开下面的链接，%s（访问有效期：%s）：\n%s",
		inv.ExpiresAt.Local().Format("01-02 15:04"), action, protocol.DescribeLeaseDuration(inv.LeaseDuration), link))
	if inv.MaxUses == 1 {
		b.WriteString("\n此链接仅限 1 台设备使用，请勿转发。")
	} else if inv.MaxUses > 1 {
		b.WriteString(fmt.Sprintf("\n此链接最多 %d 台设备使用，请勿转发。", inv.MaxUses))
	}
	b.WriteString("\n授权结束后 2 小时内，可以重新打开此链接申请延长。")
	return b.String()
}

func isAccessPath(p string) bool {
	return p == "leases" || p == "invites" || p == "requests"
}

// handleAccess serves the access endpoints shared by tunnels and groups:
//
//	POST   leases/revoke            {"ip": "..."}（ip 为空表示撤销全部）
//	POST   leases/extend            {"ip": "...", "minutes": 60} 延长授权（同时批准该设备的延长申请）
//	POST   invites                  新建邀请
//	DELETE invites/{code}           作废邀请
//	GET    invites/{code}/share     分享文案
//	POST   requests/{rid}           {"approve": true|false}
func (s *APIServer) handleAccess(w http.ResponseWriter, r *http.Request, sc Scope, rest []string) {
	switch {
	case len(rest) == 2 && rest[0] == "leases" && rest[1] == "revoke" && r.Method == http.MethodPost:
		var req struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if err := s.agent.RevokeLease(sc, strings.TrimSpace(req.IP)); err != nil {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "revoked"})

	case len(rest) == 2 && rest[0] == "leases" && rest[1] == "extend" && r.Method == http.MethodPost:
		var req struct {
			IP      string `json:"ip"`
			Minutes int    `json:"minutes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if req.Minutes < 1 || req.Minutes > protocol.MaxLeaseMinutes {
			jsonError(w, http.StatusBadRequest, "延长时长须在 1 分钟到 30 天之间")
			return
		}
		exp, err := s.extendAccess(sc, strings.TrimSpace(req.IP), req.Minutes)
		if err != nil {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{"expires_at": exp})

	case rest[0] == "invites":
		s.handleInvites(w, r, sc, rest[1:])

	case len(rest) == 2 && rest[0] == "requests" && r.Method == http.MethodPost:
		var req struct {
			Approve bool `json:"approve"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		if err := s.agent.DecideRequest(sc, rest[1], req.Approve); err != nil {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})

	default:
		http.NotFound(w, r)
	}
}

// extendAccess extends a device's access in a tunnel or group. Closed tunnels are reopened first
// (a group reopens all its members), and each is kept open at least until the new expiry.
func (s *APIServer) extendAccess(sc Scope, ip string, minutes int) (time.Time, error) {
	if ip == "" {
		return time.Time{}, fmt.Errorf("缺少设备 IP")
	}
	var members []protocol.TunnelConfig
	if sc.GroupID != "" {
		members = s.cm.GroupMembers(sc.GroupID)
	} else if t, ok := s.cm.FindTunnel(sc.TunnelID); ok {
		members = []protocol.TunnelConfig{t}
	}
	if len(members) == 0 {
		return time.Time{}, fmt.Errorf("通道不存在或组内没有通道")
	}
	if !s.canExtend(sc, ip) {
		return time.Time{}, fmt.Errorf("这台设备没有可延长的授权（访问结束已超过 2 小时或已被撤销）")
	}
	on := true
	for _, t := range members {
		if !t.Enabled {
			if _, err := s.setEnabled(t.ID, &on); err != nil {
				return time.Time{}, err
			}
		}
	}
	exp, err := s.agent.ExtendLease(sc, ip, minutes)
	if err != nil {
		return time.Time{}, err
	}
	for _, t := range members {
		if updated, changed, err := s.cm.ExtendUntil(t.ID, exp); err == nil && changed {
			s.agent.SyncTunnel(updated, true)
		}
	}
	return exp, nil
}

// canExtend reports whether ip has a running lease, or an access ended within two hours, in the scope.
// 先检查再开启通道，避免对没有可延长授权的设备白白打开通道。
func (s *APIServer) canExtend(sc Scope, ip string) bool {
	metrics := s.agent.GetLatestMetrics()
	for _, r := range renewalsOf(metrics.Renewals, sc.TunnelID, sc.GroupID) {
		if r.IP == ip {
			return true
		}
	}
	var leases []protocol.DynamicLease
	if sc.GroupID != "" {
		for _, g := range metrics.Groups {
			if g.GroupID == sc.GroupID {
				leases = g.DynamicLeases
			}
		}
	} else {
		for _, t := range metrics.Tunnels {
			if t.TunnelID == sc.TunnelID {
				leases = t.DynamicLeases
			}
		}
	}
	for _, l := range leases {
		if l.IP == ip {
			return true
		}
	}
	return false
}

// scopeShare builds the share message for an invite of a tunnel or group
func (s *APIServer) scopeShare(cfg Config, sc Scope, inv protocol.Invite, link string) string {
	if sc.GroupID != "" {
		g, _ := s.cm.FindGroup(sc.GroupID)
		var open []protocol.TunnelConfig
		for _, t := range s.cm.GroupMembers(sc.GroupID) {
			if t.Enabled {
				open = append(open, t)
			}
		}
		return groupShareText(cfg, g, open, inv, link)
	}
	t, _ := s.cm.FindTunnel(sc.TunnelID)
	return shareText(cfg, t, inv, link)
}

// scopeOpen reports whether the tunnel, or at least one tunnel of the group, is enabled
func (s *APIServer) scopeOpen(sc Scope) bool {
	if sc.GroupID != "" {
		for _, t := range s.cm.GroupMembers(sc.GroupID) {
			if t.Enabled {
				return true
			}
		}
		return false
	}
	t, ok := s.cm.FindTunnel(sc.TunnelID)
	return ok && t.Enabled
}

// scopeInvites returns the live invites of a tunnel or group
func scopeInvites(m protocol.MetricsMessage, sc Scope) []protocol.Invite {
	if sc.GroupID != "" {
		for _, g := range m.Groups {
			if g.GroupID == sc.GroupID {
				return g.Invites
			}
		}
		return nil
	}
	for _, t := range m.Tunnels {
		if t.TunnelID == sc.TunnelID {
			return t.Invites
		}
	}
	return nil
}

func (s *APIServer) handleInvites(w http.ResponseWriter, r *http.Request, sc Scope, rest []string) {
	cfg := s.cm.Get()

	switch {
	case len(rest) == 0 && r.Method == http.MethodPost:
		var req struct {
			Note            string `json:"note"`
			ValidMinutes    int    `json:"valid_minutes"`
			MaxUses         *int   `json:"max_uses"`
			LeaseDuration   string `json:"lease_duration"` // 授权后可访问时长，默认 2 小时
			RequireApproval bool   `json:"require_approval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		// 默认：24 小时内有效、限 1 台设备
		if req.ValidMinutes == 0 {
			req.ValidMinutes = 24 * 60
		}
		maxUses := 1
		if req.MaxUses != nil {
			maxUses = *req.MaxUses
		}
		if !protocol.ValidLeaseDuration(req.LeaseDuration) {
			jsonError(w, http.StatusBadRequest, "无效的授权时长")
			return
		}
		if req.LeaseDuration == "" {
			req.LeaseDuration = protocol.LeaseDuration2h
		}
		if !s.scopeOpen(sc) {
			msg := "通道未开启，请先开启通道再生成邀请链接"
			if sc.GroupID != "" {
				msg = "通道组内没有开启的通道，请先开启再生成邀请链接"
			}
			jsonError(w, http.StatusConflict, msg)
			return
		}
		inv, err := s.agent.CreateInvite(sc, strings.TrimSpace(req.Note), req.ValidMinutes, maxUses, req.LeaseDuration, req.RequireApproval)
		if err != nil {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		link := s.inviteBase(cfg) + inv.Code
		jsonResponse(w, http.StatusCreated, map[string]any{
			"invite":     inv,
			"link":       link,
			"share_text": s.scopeShare(cfg, sc, inv, link),
		})

	case len(rest) == 1 && r.Method == http.MethodDelete:
		if err := s.agent.RevokeInvite(sc, rest[0]); err != nil {
			jsonError(w, http.StatusConflict, err.Error())
			return
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "revoked"})

	case len(rest) == 2 && rest[1] == "share" && r.Method == http.MethodGet:
		for _, inv := range scopeInvites(s.agent.GetLatestMetrics(), sc) {
			if inv.Code == rest[0] {
				link := s.inviteBase(cfg) + inv.Code
				jsonResponse(w, http.StatusOK, map[string]any{"invite": inv, "link": link, "share_text": s.scopeShare(cfg, sc, inv, link)})
				return
			}
		}
		jsonError(w, http.StatusNotFound, "邀请链接不存在或已过期")

	default:
		http.NotFound(w, r)
	}
}

func (s *APIServer) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// mode=check：仅在会话不健康时重连（系统唤醒时使用）
	force := r.URL.Query().Get("mode") != "check"
	go s.agent.Reconnect(force)
	jsonResponse(w, http.StatusOK, map[string]string{"status": "reconnecting"})
}

func (s *APIServer) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target string `json:"target"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	req.Target = strings.TrimSpace(req.Target)
	if req.Target == "" {
		jsonError(w, http.StatusBadRequest, "target is required")
		return
	}
	res := ProbeTarget(req.Target, 2*time.Second)
	jsonResponse(w, http.StatusOK, map[string]any{
		"target":    res.Target,
		"reachable": res.Reachable,
		"rtt":       res.FormatRTT(),
		"error":     res.Error,
	})
}

func (s *APIServer) handleRecommendPort(w http.ResponseWriter, r *http.Request) {
	port := s.cm.RecommendPort()
	if port == 0 {
		jsonError(w, http.StatusConflict, "端口池已无空闲端口")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]int{"recommended_port": port})
}

// handleConnections returns live client connections (optionally for one tunnel) and recent security events
func (s *APIServer) handleConnections(w http.ResponseWriter, r *http.Request) {
	metrics := s.agent.GetLatestMetrics()
	match := s.tunnelFilter(r)
	conns := make([]protocol.ActiveConnInfo, 0, len(metrics.ActiveConns))
	for _, c := range metrics.ActiveConns {
		if match(c.TunnelID) {
			conns = append(conns, c)
		}
	}
	events := metrics.SecurityEvents
	if events == nil {
		events = []protocol.SecurityEvent{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"active_conns":    conns,
		"security_events": events,
	})
}

func (s *APIServer) handleKillConn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ConnID string `json:"conn_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ConnID != "" {
		s.agent.KillConn(req.ConnID)
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *APIServer) handleClientHistory(w http.ResponseWriter, r *http.Request) {
	match := s.tunnelFilter(r)
	out := make([]protocol.ClientHistoryRecord, 0)
	for _, rec := range s.cm.GetClientHistory("") {
		if match(rec.TunnelID) {
			out = append(out, rec)
		}
	}
	jsonResponse(w, http.StatusOK, out)
}

// tunnelFilter matches tunnels selected by ?tunnel_id= or ?group_id= (everything when neither is set)
func (s *APIServer) tunnelFilter(r *http.Request) func(id string) bool {
	if gid := r.URL.Query().Get("group_id"); gid != "" {
		ids := make(map[string]bool)
		for _, t := range s.cm.GroupMembers(gid) {
			ids[t.ID] = true
		}
		return func(id string) bool { return ids[id] }
	}
	if tid := r.URL.Query().Get("tunnel_id"); tid != "" {
		return func(id string) bool { return id == tid }
	}
	return func(string) bool { return true }
}

func (s *APIServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.cm.Get()
		jsonResponse(w, http.StatusOK, map[string]string{
			"server_addr": cfg.ServerAddr,
			"token":       cfg.Token,
			"public_host": cfg.PublicHostOrDefault(),
		})
	case http.MethodPost:
		var req struct {
			ServerAddr string  `json:"server_addr"`
			Token      string  `json:"token"`
			PublicHost *string `json:"public_host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			jsonError(w, http.StatusBadRequest, "Invalid JSON")
			return
		}
		publicHost := s.cm.Get().PublicHost
		if req.PublicHost != nil {
			publicHost = *req.PublicHost
		}
		changed, err := s.cm.UpdateServer(req.ServerAddr, req.Token, publicHost)
		if err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		// 新地址/令牌必须重连才会生效；只改对外访问地址时不打断连接
		if changed {
			go s.agent.Reconnect(true)
		}
		jsonResponse(w, http.StatusOK, map[string]string{"status": "updated"})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *APIServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func() bool {
		metrics := s.agent.GetLatestMetrics()
		payload := s.statusPayload()
		payload["metrics"] = metrics
		payload["tunnels"] = s.tunnelsWithMetrics(metrics)
		payload["groups"] = s.groupsView(metrics)
		data, _ := json.Marshal(payload)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}

func (s *APIServer) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.OnShutdown == nil {
		jsonError(w, http.StatusNotImplemented, "shutdown not supported")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"status": "shutting down"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go s.OnShutdown()
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	jsonResponse(w, status, map[string]string{"error": msg})
}

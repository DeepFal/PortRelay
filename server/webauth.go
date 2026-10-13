package server

import (
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/DeepFal/PortRelay/core/protocol"
)

const maxNameRunes = 32

func (m *Manager) handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/health":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","server":"PortRelay"}`))
	case strings.HasPrefix(path, "/i/") && r.Method != http.MethodPost:
		m.serveInviteForm(w, r, strings.TrimPrefix(path, "/i/"))
	case path == "/auth" && r.Method == http.MethodPost:
		m.serveInviteSubmit(w, r)
	case path == "/auth":
		m.serveInviteForm(w, r, r.URL.Query().Get("code"))
	case path == "/auth/status":
		m.serveRequestStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

func clientIPOf(r *http.Request) string {
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = h
	}
	if parsed := net.ParseIP(ip); parsed != nil {
		return parsed.String()
	}
	return ""
}

// accessHost returns the host LAN users should connect to: the configured public host,
// otherwise the host the visitor used to open the link (it reached this server).
func (m *Manager) accessHost(r *http.Request) string {
	if m.PublicHost != "" {
		return m.PublicHost
	}
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host
}

// inviteTarget resolves an invite code to its (currently registered) tunnel
func (m *Manager) inviteTarget(code string) (*Tunnel, protocol.Invite, string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, protocol.Invite{}, "缺少邀请码"
	}
	tunnelID, inv, ok := m.state.findInvite(code)
	if !ok {
		return nil, protocol.Invite{}, "链接无效或已过期，请向管理员索取新的邀请链接"
	}
	t := m.tunnel(tunnelID)
	if t == nil {
		return nil, protocol.Invite{}, "对应通道当前未开启，请联系管理员"
	}
	return t, inv, ""
}

func (m *Manager) serveInviteForm(w http.ResponseWriter, r *http.Request, code string) {
	ip := clientIPOf(r)
	t, inv, problem := m.inviteTarget(code)
	if problem == "" && ip == "" {
		problem = "无法识别客户端 IP"
	}
	if problem == "" {
		if err := m.state.checkInvite(t.ID(), inv.Code, ip); err != nil {
			problem = err.Error()
		}
	}
	if problem != "" {
		m.writePage(w, r, http.StatusNotFound, pageError, "无法使用此链接", problemHTML(problem))
		return
	}

	cfg := t.Snapshot()
	action := "确认并获取访问权限"
	approvalTip := ""
	if inv.RequireApproval {
		action = "提交访问申请"
		approvalTip = `<div class="tip">此邀请需要管理员审批，提交后请保持此页面打开，批准后会自动显示结果。</div>`
	}
	body := fmt.Sprintf(`
		%s
		<form method="post" action="/auth" class="form">
		  <input type="hidden" name="code" value="%s">
		  <label for="name">您的姓名</label>
		  <input id="name" name="name" required maxlength="%d" placeholder="例如：张三" autofocus>
		  <button type="submit">%s</button>
		</form>
		%s`,
		m.detailRows(r, cfg, ip, durationText(inv.LeaseDuration)),
		html.EscapeString(inv.Code), maxNameRunes, action, approvalTip)
	m.writePage(w, r, http.StatusOK, pageForm, "申请访问 "+cfg.Name, body)
}

func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return truncateRunes(s, maxNameRunes)
}

func (m *Manager) serveInviteSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = io.NopCloser(io.LimitReader(r.Body, 8<<10))
	if err := r.ParseForm(); err != nil {
		m.writePage(w, r, http.StatusBadRequest, pageError, "请求无效", problemHTML("表单内容无法解析"))
		return
	}
	ip := clientIPOf(r)
	name := sanitizeName(r.PostForm.Get("name"))
	t, inv, problem := m.inviteTarget(r.PostForm.Get("code"))
	switch {
	case problem != "":
	case ip == "":
		problem = "无法识别客户端 IP"
	case name == "":
		problem = "请填写您的姓名"
	}
	if problem != "" {
		m.writePage(w, r, http.StatusBadRequest, pageError, "无法完成授权", problemHTML(problem))
		return
	}

	cfg := t.Snapshot()
	if inv.RequireApproval {
		req, err := m.state.addRequest(cfg.ID, inv.Code, name, ip)
		if err != nil {
			m.writePage(w, r, http.StatusConflict, pageError, "无法提交申请", problemHTML(err.Error()))
			return
		}
		log.Printf("[WebAuth] Access request from %s (%s) on tunnel %s awaiting approval", name, ip, cfg.Name)
		http.Redirect(w, r, "/auth/status?id="+url.QueryEscape(req.ID), http.StatusSeeOther)
		return
	}

	lease, err := m.state.useInviteAndGrant(cfg.ID, inv.Code, name, ip, cfg.ExpiresAt)
	if err != nil {
		m.writePage(w, r, http.StatusConflict, pageError, "无法完成授权", problemHTML(err.Error()))
		return
	}
	t.AddDynamicWhitelist(ip, lease.ExpiresAt)
	log.Printf("[WebAuth] Granted %s (%s) access to tunnel %s (port %d): %s",
		name, ip, cfg.Name, cfg.ListenPort, leaseValidity(lease))
	m.writePage(w, r, http.StatusOK, pageSuccess, "授权成功", m.successBody(r, cfg, ip, lease))
}

func (m *Manager) serveRequestStatus(w http.ResponseWriter, r *http.Request) {
	req, ok := m.state.request(r.URL.Query().Get("id"))
	if !ok {
		m.writePage(w, r, http.StatusNotFound, pageError, "申请已失效", problemHTML("申请不存在或已过期，请重新打开邀请链接提交"))
		return
	}
	t := m.tunnel(req.TunnelID)
	switch {
	case req.Status == "rejected":
		m.writePage(w, r, http.StatusOK, pageError, "申请未通过", problemHTML("管理员拒绝了您的访问申请"))
	case t == nil:
		m.writePage(w, r, http.StatusOK, pageError, "通道未开启", problemHTML("对应通道当前未开启，请联系管理员"))
	case req.Status == "approved":
		cfg := t.Snapshot()
		lease, _ := m.state.lease(cfg.ID, req.IP)
		m.writePage(w, r, http.StatusOK, pageSuccess, "申请已批准", m.successBody(r, cfg, req.IP, lease))
	default:
		cfg := t.Snapshot()
		duration := ""
		if _, inv, ok := m.state.findInvite(req.Invite); ok {
			duration = inv.LeaseDuration
		}
		body := m.detailRows(r, cfg, req.IP, durationText(duration)) +
			fmt.Sprintf(`<div class="tip">%s，您的申请已提交，正在等待管理员批准。此页面会自动刷新。</div>`, html.EscapeString(req.Name))
		w.Header().Set("Refresh", "5")
		m.writePage(w, r, http.StatusOK, pagePending, "等待管理员批准", body)
	}
}

// durationText describes how long access lasts once granted
func durationText(d string) string {
	if d == protocol.LeaseDurationTunnel {
		return protocol.DescribeLeaseDuration(d)
	}
	return protocol.DescribeLeaseDuration(d) + "（从首次连接开始计算）"
}

// leaseValidity describes a granted lease: not yet started, or running until its expiry
func leaseValidity(l leaseInfo) string {
	if l.Deferred {
		return fmt.Sprintf("首次连接后 %s，请在 %s 前连接", protocol.DescribeLeaseDuration(l.Duration), l.ExpiresAt.Format("01-02 15:04"))
	}
	return "至 " + l.ExpiresAt.Format("01-02 15:04")
}

func (m *Manager) successBody(r *http.Request, cfg protocol.TunnelConfig, ip string, l leaseInfo) string {
	return m.detailRows(r, cfg, ip, leaseValidity(l)) +
		`<div class="tip">您的设备已获得访问权限，现在可以直接用客户端连接上面的访问地址，计时从首次连接开始。到期后如需继续使用，请向管理员索取新的邀请链接。</div>`
}

// detailRows renders the tunnel info table
func (m *Manager) detailRows(r *http.Request, cfg protocol.TunnelConfig, ip string, validity string) string {
	validity = html.EscapeString(validity)
	return fmt.Sprintf(`
		<div class="row"><span class="label">目标通道</span><span class="value">%s</span></div>
		<div class="row"><span class="label">访问地址</span><span class="value mono">%s:%d</span></div>
		<div class="row"><span class="label">您的设备 IP</span><span class="value mono">%s</span></div>
		<div class="row"><span class="label">访问有效期</span><span class="value highlight">%s</span></div>`,
		html.EscapeString(cfg.Name), html.EscapeString(m.accessHost(r)), cfg.ListenPort, html.EscapeString(ip), validity)
}

func problemHTML(msg string) string {
	return fmt.Sprintf(`<div class="tip error">%s</div>`, html.EscapeString(msg))
}

type pageKind int

const (
	pageSuccess pageKind = iota
	pageError
	pagePending
	pageForm
)

func (m *Manager) writePage(w http.ResponseWriter, r *http.Request, status int, kind pageKind, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(renderPage(kind, title, body)))
}

func renderPage(kind pageKind, title, body string) string {
	color := "#34C759"
	icon := `<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#34C759" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"></path><polyline points="22 4 12 14.01 9 11.01"></polyline></svg>`
	switch kind {
	case pageError:
		color = "#FF3B30"
		icon = `<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#FF3B30" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><line x1="15" y1="9" x2="9" y2="15"></line><line x1="9" y1="9" x2="15" y2="15"></line></svg>`
	case pagePending:
		color = "#FF9500"
		icon = `<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#FF9500" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"></circle><polyline points="12 6 12 12 16 14"></polyline></svg>`
	case pageForm:
		color = "#007AFF"
		icon = `<svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#007AFF" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2" ry="2"></rect><path d="M7 11V7a5 5 0 0 1 10 0v4"></path></svg>`
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s - PortRelay</title>
<style>
  :root { --bg:#F5F5F7; --card:#FFFFFF; --text:#1D1D1F; --sub:#86868B; --border:#D2D2D7; --code-bg:#E8E8ED; --accent:%s; }
  @media (prefers-color-scheme: dark) {
    :root { --bg:#000000; --card:#1C1C1E; --text:#F5F5F7; --sub:#8E8E93; --border:#38383A; --code-bg:#2C2C2E; }
  }
  body { margin:0; padding:24px 16px; background:var(--bg); color:var(--text);
    font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
    display:flex; justify-content:center; align-items:center; min-height:90vh; }
  .card { background:var(--card); border:1px solid var(--border); border-radius:16px; max-width:440px; width:100%%;
    padding:32px 24px; box-shadow:0 4px 24px rgba(0,0,0,0.06); box-sizing:border-box; }
  .header { text-align:center; margin-bottom:24px; }
  .title { font-size:20px; font-weight:700; margin-top:12px; margin-bottom:4px; }
  .sub { font-size:13px; color:var(--sub); }
  .row { display:flex; justify-content:space-between; align-items:center; gap:12px; padding:10px 0; border-bottom:1px solid var(--border); font-size:13px; }
  .label { color:var(--sub); white-space:nowrap; }
  .value { font-weight:600; text-align:right; word-break:break-all; }
  .mono { font-family:ui-monospace,Menlo,Monaco,Consolas,monospace; background:var(--code-bg); padding:2px 6px; border-radius:4px; }
  .highlight { color:var(--accent); }
  .tip { margin-top:20px; padding:12px 14px; background:var(--code-bg); border-radius:8px; font-size:12px; line-height:1.5; color:var(--sub); }
  .tip.error { color:#FF3B30; background:rgba(255,59,48,0.1); font-weight:600; text-align:center; }
  .form { margin-top:20px; display:flex; flex-direction:column; gap:8px; }
  .form label { font-size:13px; color:var(--sub); }
  .form input { font-size:16px; padding:10px 12px; border-radius:8px; border:1px solid var(--border); background:var(--bg); color:var(--text); }
  .form button { margin-top:8px; font-size:15px; font-weight:600; padding:11px; border:0; border-radius:8px; background:var(--accent); color:#fff; cursor:pointer; }
  .footer { text-align:center; margin-top:24px; font-size:11px; color:var(--sub); }
</style>
</head>
<body>
<div class="card">
  <div class="header">
    %s
    <div class="title">%s</div>
    <div class="sub">PortRelay 端口访问授权</div>
  </div>
  %s
  <div class="footer">PortRelay 安全防护 · 临时访问通行证</div>
</div>
</body>
</html>`, html.EscapeString(title), color, icon, html.EscapeString(title), body)
}

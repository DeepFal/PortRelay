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
	"time"
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
	case path == "/renew" && r.Method == http.MethodPost:
		m.serveRenewSubmit(w, r)
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

// accessScope is what an invite or request grants access to: one tunnel or a whole tunnel group
type accessScope struct {
	key     string
	group   bool
	name    string
	tunnels []protocol.TunnelConfig // 当前开启的通道，按端口排序
	expires *time.Time
}

// resolveScope looks up the open tunnels of a scope; false when none is open
func (m *Manager) resolveScope(scope string) (accessScope, bool) {
	tunnels := m.scopeTunnels(scope)
	if len(tunnels) == 0 {
		return accessScope{}, false
	}
	_, isGroup := splitScope(scope)
	sc := accessScope{key: scope, group: isGroup, expires: scopeExpiresOf(tunnels)}
	for _, t := range tunnels {
		sc.tunnels = append(sc.tunnels, t.Snapshot())
	}
	sc.name = sc.tunnels[0].Name
	if isGroup {
		sc.name = sc.tunnels[0].GroupName
		if sc.name == "" {
			sc.name = "通道组"
		}
	}
	return sc, true
}

// inviteTarget resolves an invite code to the tunnel or group it grants access to
func (m *Manager) inviteTarget(code string) (accessScope, protocol.Invite, string) {
	code = strings.TrimSpace(code)
	if code == "" {
		return accessScope{}, protocol.Invite{}, "缺少邀请码"
	}
	scope, inv, ok := m.state.findInvite(code)
	if !ok {
		return accessScope{}, protocol.Invite{}, "链接无效或已过期，请向管理员索取新的邀请链接"
	}
	sc, ok := m.resolveScope(scope)
	if !ok {
		return accessScope{}, protocol.Invite{}, "对应通道当前未开启，请联系管理员"
	}
	return sc, inv, ""
}

func (m *Manager) serveInviteForm(w http.ResponseWriter, r *http.Request, code string) {
	ip := clientIPOf(r)
	// 用过这个链接的设备：显示它的授权状态，到期前后都可以申请延长（链接或通道失效后也一样）
	if ip != "" {
		if da, ok := m.state.deviceAccess(strings.TrimSpace(code), ip); ok {
			m.serveDeviceAccess(w, r, strings.TrimSpace(code), ip, da)
			return
		}
	}
	sc, inv, problem := m.inviteTarget(code)
	if problem == "" && ip == "" {
		problem = "无法识别客户端 IP"
	}
	if problem == "" {
		if err := m.state.checkInvite(sc.key, inv.Code, ip); err != nil {
			problem = err.Error()
		}
	}
	if problem != "" {
		m.writePage(w, r, http.StatusNotFound, pageError, "无法使用此链接", problemHTML(problem))
		return
	}

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
		m.detailRows(r, sc, ip, durationText(inv.LeaseDuration)),
		html.EscapeString(inv.Code), maxNameRunes, action, approvalTip)
	m.writePage(w, r, http.StatusOK, pageForm, "申请访问 "+sc.name, body)
}

// serveDeviceAccess shows a returning device its access and, when it can, the renewal button
func (m *Manager) serveDeviceAccess(w http.ResponseWriter, r *http.Request, code, ip string, da deviceAccess) {
	if da.pending != nil {
		http.Redirect(w, r, "/auth/status?id="+url.QueryEscape(da.pending.ID), http.StatusSeeOther)
		return
	}
	sc, open := m.resolveScope(da.scope)
	rows := func(validity string) string {
		if open {
			return m.detailRows(r, sc, ip, validity)
		}
		return closedRows(da, ip, validity)
	}
	renewForm := fmt.Sprintf(`
		<form method="post" action="/renew" class="form">
		  <input type="hidden" name="code" value="%s">
		  <button type="submit">申请延长</button>
		</form>`, html.EscapeString(code))
	switch {
	case da.lease != nil && da.lease.Deferred && open:
		m.writePage(w, r, http.StatusOK, pageSuccess, "已获得访问权限", m.successBody(r, sc, ip, *da.lease))
	case da.lease != nil && da.lease.Deferred:
		m.writePage(w, r, http.StatusOK, pageSuccess, "已获得访问权限", rows(leaseValidity(*da.lease)))
	case da.lease != nil:
		left := time.Until(da.lease.ExpiresAt).Round(time.Minute)
		body := rows(fmt.Sprintf("至 %s（剩余 %s）", da.lease.ExpiresAt.Format("01-02 15:04"), protocol.DescribeMinutes(max(1, int(left/time.Minute))))) +
			`<div class="tip">需要更多时间时可以申请延长，管理员批准后在原到期时间上顺延。</div>` + renewForm
		m.writePage(w, r, http.StatusOK, pageSuccess, "访问已授权", body)
	case da.ended != nil && da.ended.Revoked:
		m.writePage(w, r, http.StatusForbidden, pageError, "授权已撤销", problemHTML(errRevoked.Error()))
	case da.ended != nil:
		closedTip := ""
		if !open {
			closedTip = "通道当前已关闭，批准后会自动重新开启。"
		}
		body := rows("已于 "+da.ended.EndedAt.Format("01-02 15:04")+" 结束") +
			fmt.Sprintf(`<div class="tip">可在 %s 前申请延长，管理员批准并确定时长后即可继续使用。%s</div>`,
				da.ended.EndedAt.Add(renewWindow).Format("15:04"), closedTip) + renewForm
		m.writePage(w, r, http.StatusOK, pageForm, "访问已结束", body)
	default:
		m.writePage(w, r, http.StatusGone, pageError, "访问已结束", problemHTML(errNothingToRenew.Error()))
	}
}

// closedRows describes a scope whose tunnels are all closed (only its last known name is left)
func closedRows(da deviceAccess, ip, validity string) string {
	label := "目标通道"
	if _, isGroup := splitScope(da.scope); isGroup {
		label = "目标通道组"
	}
	name := da.name
	if name == "" {
		name = "（已关闭）"
	}
	return fmt.Sprintf(`
		<div class="row"><span class="label">%s</span><span class="value">%s</span></div>
		<div class="row"><span class="label">您的设备 IP</span><span class="value mono">%s</span></div>
		<div class="row"><span class="label">访问有效期</span><span class="value highlight">%s</span></div>`,
		label, html.EscapeString(name), html.EscapeString(ip), html.EscapeString(validity))
}

func (m *Manager) serveRenewSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = io.NopCloser(io.LimitReader(r.Body, 8<<10))
	if err := r.ParseForm(); err != nil {
		m.writePage(w, r, http.StatusBadRequest, pageError, "请求无效", problemHTML("表单内容无法解析"))
		return
	}
	ip := clientIPOf(r)
	da, ok := m.state.deviceAccess(strings.TrimSpace(r.PostForm.Get("code")), ip)
	if ip == "" || !ok {
		m.writePage(w, r, http.StatusNotFound, pageError, "无法申请延长", problemHTML("没有找到这台设备的授权，请联系管理员获取新的邀请链接"))
		return
	}
	req, err := m.state.addRenewal(da.scope, ip)
	if err != nil {
		m.writePage(w, r, http.StatusConflict, pageError, "无法申请延长", problemHTML(err.Error()))
		return
	}
	log.Printf("[WebAuth] %s (%s) asked to extend access to %s", req.Name, ip, da.name)
	http.Redirect(w, r, "/auth/status?id="+url.QueryEscape(req.ID), http.StatusSeeOther)
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
	sc, inv, problem := m.inviteTarget(r.PostForm.Get("code"))
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

	if inv.RequireApproval {
		req, err := m.state.addRequest(sc.key, inv.Code, name, ip)
		if err != nil {
			m.writePage(w, r, http.StatusConflict, pageError, "无法提交申请", problemHTML(err.Error()))
			return
		}
		log.Printf("[WebAuth] Access request from %s (%s) on %s awaiting approval", name, ip, sc.name)
		http.Redirect(w, r, "/auth/status?id="+url.QueryEscape(req.ID), http.StatusSeeOther)
		return
	}

	lease, err := m.state.useInviteAndGrant(sc.key, inv.Code, name, ip, sc.expires)
	if err != nil {
		m.writePage(w, r, http.StatusConflict, pageError, "无法完成授权", problemHTML(err.Error()))
		return
	}
	m.syncScope(sc.key)
	log.Printf("[WebAuth] Granted %s (%s) access to %s (%d port(s)): %s",
		name, ip, sc.name, len(sc.tunnels), leaseValidity(lease))
	m.writePage(w, r, http.StatusOK, pageSuccess, "授权成功", m.successBody(r, sc, ip, lease))
}

func (m *Manager) serveRequestStatus(w http.ResponseWriter, r *http.Request) {
	req, ok := m.state.request(r.URL.Query().Get("id"))
	if !ok {
		m.writePage(w, r, http.StatusNotFound, pageError, "申请已失效", problemHTML("申请不存在或已过期，请重新打开邀请链接提交"))
		return
	}
	scope := scopeOf(req.TunnelID, req.GroupID)
	sc, open := m.resolveScope(scope)
	switch {
	case req.Status == "rejected" && req.Renewal:
		m.writePage(w, r, http.StatusOK, pageError, "申请未通过", problemHTML("管理员拒绝了您的延长申请"))
	case req.Status == "rejected":
		m.writePage(w, r, http.StatusOK, pageError, "申请未通过", problemHTML("管理员拒绝了您的访问申请"))
	case req.Renewal && req.Status == "approved" && !open:
		// 批准后 Agent 正在重新开启通道
		w.Header().Set("Refresh", "3")
		m.writePage(w, r, http.StatusOK, pagePending, "已批准，正在开启通道", `<div class="tip">管理员已批准延长，通道正在重新开启，此页面会自动刷新。</div>`)
	case req.Renewal && !open:
		da := deviceAccess{scope: scope, name: m.state.scopeDisplayName(scope)}
		w.Header().Set("Refresh", "5")
		m.writePage(w, r, http.StatusOK, pagePending, "等待管理员批准", closedRows(da, req.IP, "等待批准")+
			fmt.Sprintf(`<div class="tip">%s，您的延长申请已提交，管理员批准时会确定延长多久；通道当前已关闭，批准后会自动重新开启。此页面会自动刷新。</div>`, html.EscapeString(req.Name)))
	case !open:
		m.writePage(w, r, http.StatusOK, pageError, "通道未开启", problemHTML("对应通道当前未开启，请联系管理员"))
	case req.Status == "approved" && req.Renewal:
		lease, _ := m.state.lease(sc.key, req.IP)
		m.writePage(w, r, http.StatusOK, pageSuccess, "延长已批准", m.detailRows(r, sc, req.IP, leaseValidity(lease))+
			`<div class="tip">管理员已批准延长，现在可以继续使用。到期后 2 小时内还可以重新打开邀请链接申请延长。</div>`)
	case req.Status == "approved":
		lease, _ := m.state.lease(sc.key, req.IP)
		m.writePage(w, r, http.StatusOK, pageSuccess, "申请已批准", m.successBody(r, sc, req.IP, lease))
	case req.Renewal:
		w.Header().Set("Refresh", "5")
		m.writePage(w, r, http.StatusOK, pagePending, "等待管理员批准", m.detailRows(r, sc, req.IP, "等待批准")+
			fmt.Sprintf(`<div class="tip">%s，您的延长申请已提交，管理员批准时会确定延长多久。此页面会自动刷新。</div>`, html.EscapeString(req.Name)))
	default:
		duration := ""
		if _, inv, ok := m.state.findInvite(req.Invite); ok {
			duration = inv.LeaseDuration
		}
		body := m.detailRows(r, sc, req.IP, durationText(duration)) +
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

func (m *Manager) successBody(r *http.Request, sc accessScope, ip string, l leaseInfo) string {
	where := "上面的访问地址"
	if sc.group {
		where = "上面任意一个访问地址"
	}
	return m.detailRows(r, sc, ip, leaseValidity(l)) +
		`<div class="tip">您的设备已获得访问权限，现在可以直接用客户端连接` + where + `，计时从首次连接开始。到期后 2 小时内，可以重新打开这个邀请链接申请延长。</div>`
}

// detailRows renders the info table; a group lists every access address with its target
func (m *Manager) detailRows(r *http.Request, sc accessScope, ip string, validity string) string {
	host := html.EscapeString(m.accessHost(r))
	var target, addresses string
	if sc.group {
		target = fmt.Sprintf(`<div class="row"><span class="label">目标通道组</span><span class="value">%s（%d 个地址）</span></div>`,
			html.EscapeString(sc.name), len(sc.tunnels))
		var b strings.Builder
		for _, t := range sc.tunnels {
			fmt.Fprintf(&b, `<div class="map"><span class="mono">%s:%d</span><span class="to">%s</span></div>`,
				host, t.ListenPort, html.EscapeString(t.Target))
		}
		addresses = `<div class="row stack"><span class="label">访问地址</span><div class="maps">` + b.String() + `</div></div>`
	} else {
		t := sc.tunnels[0]
		target = fmt.Sprintf(`<div class="row"><span class="label">目标通道</span><span class="value">%s</span></div>`, html.EscapeString(t.Name))
		addresses = fmt.Sprintf(`<div class="row"><span class="label">访问地址</span><span class="value mono">%s:%d</span></div>`, host, t.ListenPort)
	}
	return target + addresses + fmt.Sprintf(`
		<div class="row"><span class="label">您的设备 IP</span><span class="value mono">%s</span></div>
		<div class="row"><span class="label">访问有效期</span><span class="value highlight">%s</span></div>`,
		html.EscapeString(ip), html.EscapeString(validity))
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
  .row.stack { align-items:flex-start; }
  .maps { display:flex; flex-direction:column; gap:6px; align-items:flex-end; }
  .map { display:flex; align-items:center; gap:8px; font-weight:600; }
  .map .to { color:var(--sub); font-weight:400; font-size:12px; }
  .map .to::before { content:"→ "; }
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

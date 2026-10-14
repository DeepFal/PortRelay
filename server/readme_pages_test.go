package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DeepFal/PortRelay/core/acl"
	"github.com/DeepFal/PortRelay/core/protocol"
	"github.com/DeepFal/PortRelay/core/traffic"
)

// TestReadmePages 用模拟状态生成 README 截图用的授权页 HTML（tools/ui-snapshots/web.sh 调用），平时跳过。
// 通道只放在内存里，不监听端口；访问者 IP 直接写在请求上。
func TestReadmePages(t *testing.T) {
	out := os.Getenv("PORTRELAY_README_PAGES")
	if out == "" {
		t.Skip("PORTRELAY_README_PAGES not set")
	}
	m := NewManager(33000, 34000, testToken)
	m.PublicHost = "192.168.1.10"
	cfg := protocol.TunnelConfig{ID: "mysql", Name: "MySQL 数据库", Target: "10.0.1.16:3306", ListenPort: 33660, Enabled: true}
	m.mu.Lock()
	m.tunnels[cfg.ID] = &Tunnel{Config: cfg, whitelist: acl.NewWhitelist(nil), activeConns: map[string]*ActiveClient{},
		meter: traffic.NewMeter(), closed: make(chan struct{})}
	m.mu.Unlock()

	do := func(method, path, ip string, form url.Values) *httptest.ResponseRecorder {
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		r := httptest.NewRequest(method, "http://192.168.1.10:33000"+path, body)
		if form != nil {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		r.RemoteAddr = ip + ":52000"
		w := httptest.NewRecorder()
		m.handleHTTPRequest(w, r)
		return w
	}
	follow := func(w *httptest.ResponseRecorder, ip string) *httptest.ResponseRecorder {
		if loc := w.Header().Get("Location"); loc != "" {
			return do(http.MethodGet, loc, ip, nil)
		}
		return w
	}
	save := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if err := os.WriteFile(filepath.Join(out, name+".html"), w.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// ① 打开邀请链接 → ③ 授权成功
	inv := m.state.createInvite(cfg.ID, "张三", 24*time.Hour, 1, "2h", false)
	save("auth-form", do(http.MethodGet, "/i/"+inv.Code, "192.168.1.21", nil))
	save("auth-ok", do(http.MethodPost, "/auth", "192.168.1.21", url.Values{"code": {inv.Code}, "name": {"张三"}}))

	// ② 需要审批
	ask := m.state.createInvite(cfg.ID, "测试组", 24*time.Hour, 0, "today", true)
	save("auth-pending", follow(do(http.MethodPost, "/auth", "192.168.1.88", url.Values{"code": {ask.Code}, "name": {"李四"}}), "192.168.1.88"))

	// ④ ⑤ ⑥ 两小时前开始使用、20 分钟前到期，再打开链接申请延长，管理员批准 2 小时
	ip := "192.168.1.47"
	rn := m.state.createInvite(cfg.ID, "赵六", 24*time.Hour, 1, "2h", false)
	do(http.MethodPost, "/auth", ip, url.Values{"code": {rn.Code}, "name": {"赵六"}})
	m.state.startLease(cfg.ID, ip, nil, time.Now().Add(-140*time.Minute))
	save("auth-renew", do(http.MethodGet, "/i/"+rn.Code, ip, nil))
	status := do(http.MethodPost, "/renew", ip, url.Values{"code": {rn.Code}})
	save("auth-renew-pending", follow(status, ip))
	if resp := m.extendLease(protocol.ExtendLeaseMessage{TunnelID: cfg.ID, IP: ip, Minutes: 120}); !resp.Success {
		t.Fatal(resp.Error)
	}
	save("auth-renew-ok", follow(status, ip))
}

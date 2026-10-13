package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DeepFal/PortRelay/core/protocol"
	"github.com/DeepFal/PortRelay/server"
)

const testToken = "secret"

type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// startRelayServer runs an in-process relay server (same dispatch as cmd/portrelay-server)
func startRelayServer(t *testing.T) (string, *server.Manager) {
	t.Helper()
	m := server.NewManager(PortMin, PortMax, testToken)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				br := bufio.NewReader(c)
				p, err := br.Peek(3)
				if err != nil {
					c.Close()
					return
				}
				if s := string(p); s == "GET" || s == "POS" || s == "HEA" {
					m.HandleHTTP(&peekedConn{c, br})
					return
				}
				_ = m.HandleSession(&peekedConn{c, br})
			}(c)
		}
	}()
	return ln.Addr().String(), m
}

// startEcho runs a TCP echo server acting as the intranet target
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// freeRelayPort finds a free port inside the tunnel range
func freeRelayPort(t *testing.T, exclude ...int) int {
	t.Helper()
	skip := map[int]bool{ControlPort: true}
	for _, p := range exclude {
		skip[p] = true
	}
	for p := 33900; p < PortMax; p++ {
		if skip[p] {
			continue
		}
		if ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p)); err == nil {
			ln.Close()
			return p
		}
	}
	t.Fatal("no free port in tunnel range")
	return 0
}

func newTestConfig(t *testing.T, serverAddr, token string) *ConfigManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(Config{ServerAddr: serverAddr, Token: token, Tunnels: []protocol.TunnelConfig{}})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cm, err := NewConfigManagerAt(path)
	if err != nil {
		t.Fatal(err)
	}
	// 在临时目录删除前停止延迟写盘（Cleanup 按注册的逆序执行，Agent 会先停止）
	t.Cleanup(func() { _ = cm.Close() })
	return cm
}

type env struct {
	cm    *ConfigManager
	agent *Agent
	api   *httptest.Server
	addr  string
}

func startEnv(t *testing.T, token string) *env {
	t.Helper()
	return startEnvWith(t, token, nil)
}

func startEnvWith(t *testing.T, token string, tweak func(*Agent)) *env {
	t.Helper()
	addr, _ := startRelayServer(t)
	cm := newTestConfig(t, addr, token)
	agent := NewAgent(cm)
	if tweak != nil {
		tweak(agent)
	}
	agent.Start()
	t.Cleanup(agent.Stop)
	api := httptest.NewServer(NewAPIServer(cm, agent, nil).Handler())
	t.Cleanup(api.Close)
	return &env{cm: cm, agent: agent, api: api, addr: addr}
}

func (e *env) call(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, e.api.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (e *env) tunnel(t *testing.T, id string) TunnelWithMetrics {
	t.Helper()
	_, body := e.call(t, "GET", "/api/tunnels", nil)
	var list []TunnelWithMetrics
	_ = json.Unmarshal(body, &list)
	for _, x := range list {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("tunnel %s not found", id)
	return TunnelWithMetrics{}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func echoVia(port int, msg string) (string, error) {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		return "", err
	}
	buf := make([]byte, len(msg))
	_, err = io.ReadFull(c, buf)
	return string(buf), err
}

func TestEndToEndEditKeepsStateAndConnections(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)

	target := startEcho(t)
	port := freeRelayPort(t)
	status, body := e.call(t, "POST", "/api/tunnels", map[string]any{
		"name": "db", "target": target, "listen_port": port, "owner": "ops",
		"duration_minutes": 0, "max_bandwidth_mb": 5, "whitelist": []string{"*"},
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created protocol.TunnelConfig
	_ = json.Unmarshal(body, &created)

	waitFor(t, "tunnel online", func() bool { return e.tunnel(t, created.ID).Metrics.Status == "online" })
	if got, err := echoVia(port, "hello"); err != nil || got != "hello" {
		t.Fatalf("relay broken: %q %v", got, err)
	}

	// 编辑前生成一个邀请链接，编辑后应仍然有效
	status, body = e.call(t, "POST", "/api/tunnels/"+created.ID+"/invites", map[string]any{"max_uses": 0})
	if status != http.StatusCreated {
		t.Fatalf("create invite: %d %s", status, body)
	}
	var invResp struct {
		Link string `json:"link"`
	}
	_ = json.Unmarshal(body, &invResp)

	// 打开一条长连接，编辑后应保持可用
	long, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer long.Close()

	// Web 端只提交部分字段
	status, body = e.call(t, "PUT", "/api/tunnels/"+created.ID, map[string]any{
		"name": "db-renamed", "target": target, "listen_port": port, "whitelist": []string{"*"}, "enabled": true,
	})
	if status != http.StatusOK {
		t.Fatalf("edit: %d %s", status, body)
	}
	var edited protocol.TunnelConfig
	_ = json.Unmarshal(body, &edited)
	if edited.Owner != "ops" || edited.MaxBandwidthMB != 5 {
		t.Fatalf("partial edit wiped other fields: owner=%q bw=%v", edited.Owner, edited.MaxBandwidthMB)
	}

	_ = long.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := long.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(long, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("existing connection dropped by metadata edit: %v", err)
	}

	// 编辑后邀请链接仍然有效
	resp, err := http.Get(invResp.Link)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("invite link rejected after edit: %d", resp.StatusCode)
	}

	// 关闭后再编辑，不能被重新打开
	e.call(t, "POST", "/api/tunnels/"+created.ID+"/toggle", map[string]bool{"enabled": false})
	e.call(t, "PUT", "/api/tunnels/"+created.ID, map[string]any{"name": "x", "enabled": true})
	if got := e.tunnel(t, created.ID); got.Enabled || got.Metrics.Status != "disabled" {
		t.Fatalf("editing a disabled tunnel re-enabled it: enabled=%v status=%s", got.Enabled, got.Metrics.Status)
	}

	// 幂等开关：重复提交 enabled=false 不会翻转
	e.call(t, "POST", "/api/tunnels/"+created.ID+"/toggle", map[string]bool{"enabled": false})
	if e.tunnel(t, created.ID).Enabled {
		t.Fatal("toggle with explicit state is not idempotent")
	}
}

func TestWrongTokenIsReportedAsDisconnected(t *testing.T) {
	e := startEnv(t, "WRONG")
	waitFor(t, "auth error surfaced", func() bool { return strings.Contains(e.agent.LastError(), "鉴权") })
	if e.agent.IsConnected() {
		t.Fatal("agent reports connected with a wrong token")
	}

	// 改成正确 token 后应自动重连
	status, body := e.call(t, "POST", "/api/settings", map[string]string{"server_addr": e.addr, "token": testToken})
	if status != http.StatusOK {
		t.Fatalf("settings: %d %s", status, body)
	}
	waitFor(t, "reconnected after settings change", e.agent.IsConnected)
	if e.agent.LastError() != "" {
		t.Fatalf("last error not cleared: %s", e.agent.LastError())
	}
}

func TestRegistrationErrorIsVisible(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)

	port := freeRelayPort(t)
	blocker, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	_, body := e.call(t, "POST", "/api/tunnels", map[string]any{"name": "x", "target": "127.0.0.1:1", "listen_port": port})
	var created protocol.TunnelConfig
	_ = json.Unmarshal(body, &created)
	waitFor(t, "error status", func() bool {
		m := e.tunnel(t, created.ID).Metrics
		return m.Status == "error" && strings.Contains(m.Error, "bind")
	})
}

func TestAPIRejectsCrossOriginAndForeignHost(t *testing.T) {
	cm := newTestConfig(t, "127.0.0.1:1", testToken)
	h := NewAPIServer(cm, NewAgent(cm), nil).Handler()

	cases := []struct {
		name, host, origin string
		want               int
	}{
		{"same origin", "127.0.0.1:33080", "http://127.0.0.1:33080", 200},
		{"native app (no origin)", "127.0.0.1:33080", "", 200},
		{"localhost", "localhost:33080", "", 200},
		{"evil site", "127.0.0.1:33080", "https://evil.example", 403},
		{"dns rebinding", "evil.example:33080", "", 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest("GET", "/api/settings", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: CORS header must not be set", c.name)
		}
	}
}

func TestCorruptConfigIsBackedUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"tunnels": [`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfigManagerAt(path); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(path + ".corrupt-*")
	if len(matches) != 1 {
		t.Fatalf("expected a backup of the corrupt config, got %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != `{"tunnels": [` {
		t.Fatal("backup content differs from original")
	}
}

func TestPatchValidationAndTTL(t *testing.T) {
	cm := newTestConfig(t, "127.0.0.1:1", testToken)
	a, err := cm.AddTunnel(protocol.TunnelConfig{Name: "a", Target: "10.0.0.1:22", ListenPort: 33701, DurationMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cm.AddTunnel(protocol.TunnelConfig{Name: "b", Target: "10.0.0.1:22", ListenPort: 33701}); err == nil {
		t.Fatal("duplicate port accepted")
	}
	for _, bad := range []string{
		`{"listen_port": 33000}`,
		`{"listen_port": 40000}`,
		`{"target": "10.0.0.1"}`,
		`{"whitelist": ["192.168.1.*"]}`,
	} {
		if _, _, err := cm.PatchTunnel(a.ID, []byte(bad)); err == nil {
			t.Errorf("patch %s accepted", bad)
		}
	}

	// 改成不限时：到期时间应清除
	upd, _, err := cm.PatchTunnel(a.ID, []byte(`{"duration_minutes": 0}`))
	if err != nil {
		t.Fatal(err)
	}
	if upd.ExpiresAt != nil {
		t.Fatal("expires_at kept after switching to unlimited")
	}
	// 受保护字段不能被编辑
	upd, _, _ = cm.PatchTunnel(a.ID, []byte(`{"enabled": false, "auth_code": "hacked", "historical_bytes_in": 999}`))
	if !upd.Enabled || upd.AuthCode != a.AuthCode || upd.HistoricalBytesIn != 0 {
		t.Fatalf("protected fields modified: %+v", upd)
	}
}

func TestGetReturnsDeepCopy(t *testing.T) {
	cm := newTestConfig(t, "127.0.0.1:1", testToken)
	if _, err := cm.AddTunnel(protocol.TunnelConfig{Name: "a", Target: "10.0.0.1:22", ListenPort: 33702, Whitelist: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	snap := cm.Get()
	snap.Tunnels[0].Name = "mutated"
	snap.Tunnels[0].Whitelist[0] = "1.1.1.1"
	if got := cm.Get().Tunnels[0]; got.Name != "a" || got.Whitelist[0] != "*" {
		t.Fatal("Get() leaks internal state")
	}
}

func TestFirstRunHasNoUserData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cm, err := NewConfigManagerAt(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cm.Get()
	if cfg.ServerAddr != "" || cfg.Token != "" || len(cfg.Tunnels) != 0 {
		t.Fatalf("first-run config must be empty, got addr=%q token=%q tunnels=%d", cfg.ServerAddr, cfg.Token, len(cfg.Tunnels))
	}

	agent := NewAgent(cm)
	agent.Start()
	defer agent.Stop()
	waitFor(t, "not-configured hint", func() bool { return strings.Contains(agent.LastError(), "尚未配置") })
	if agent.IsConnected() {
		t.Fatal("agent connected without a configured server")
	}

	// 未单独设置对外访问地址时，取服务器地址的主机部分
	if _, err := cm.UpdateServer("10.1.2.3:33000", "t", ""); err != nil {
		t.Fatal(err)
	}
	if got := cm.Get().PublicHostOrDefault(); got != "10.1.2.3" {
		t.Fatalf("public host fallback = %q", got)
	}
}

func TestLeaseListAndRevoke(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)

	target := startEcho(t)
	port := freeRelayPort(t)
	// 固定白名单不包含 127.0.0.1，只有临时放通后才能访问
	_, body := e.call(t, "POST", "/api/tunnels", map[string]any{
		"name": "lease", "target": target, "listen_port": port, "whitelist": []string{"10.9.9.9"},
	})
	var created protocol.TunnelConfig
	_ = json.Unmarshal(body, &created)
	waitFor(t, "tunnel online", func() bool { return e.tunnel(t, created.ID).Metrics.Status == "online" })

	if _, err := echoVia(port, "x"); err == nil {
		t.Fatal("client outside whitelist should be blocked before web auth")
	}

	_, body = e.call(t, "POST", "/api/tunnels/"+created.ID+"/invites", map[string]any{})
	var inv struct {
		Invite protocol.Invite `json:"invite"`
	}
	_ = json.Unmarshal(body, &inv)
	resp, err := http.PostForm(fmt.Sprintf("http://%s/auth", e.addr), url.Values{"code": {inv.Invite.Code}, "name": {"张三"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	waitFor(t, "lease listed with expiry and name", func() bool {
		leases := e.tunnel(t, created.ID).Metrics.DynamicLeases
		return len(leases) == 1 && leases[0].IP == "127.0.0.1" && leases[0].Name == "张三" && time.Until(leases[0].ExpiresAt) > time.Hour
	})

	long, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer long.Close()
	_ = long.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = long.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(long, buf); err != nil {
		t.Fatalf("leased client cannot use tunnel: %v", err)
	}

	// 在线连接接口应能看到这条连接
	waitFor(t, "active connection listed", func() bool {
		_, b := e.call(t, "GET", "/api/connections?tunnel_id="+created.ID, nil)
		var out struct {
			ActiveConns []protocol.ActiveConnInfo `json:"active_conns"`
		}
		_ = json.Unmarshal(b, &out)
		return len(out.ActiveConns) == 1
	})

	status, b := e.call(t, "POST", "/api/tunnels/"+created.ID+"/leases/revoke", map[string]string{"ip": "127.0.0.1"})
	if status != http.StatusOK {
		t.Fatalf("revoke: %d %s", status, b)
	}
	waitFor(t, "lease removed", func() bool { return len(e.tunnel(t, created.ID).Metrics.DynamicLeases) == 0 })

	// 撤销后已建立的连接被断开，新连接被拒绝
	if _, err := long.Read(buf); err == nil {
		t.Fatal("existing connection of revoked IP still alive")
	}
	if _, err := echoVia(port, "x"); err == nil {
		t.Fatal("revoked IP can still connect")
	}
}

func TestLinkOnlyInviteShareAndApproval(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)

	target := startEcho(t)
	port := freeRelayPort(t)
	status, body := e.call(t, "POST", "/api/tunnels", map[string]any{
		"name": "db", "target": target, "listen_port": port,
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created protocol.TunnelConfig
	_ = json.Unmarshal(body, &created)
	if len(created.Whitelist) != 0 {
		t.Fatalf("empty whitelist must stay empty (link-only), got %v", created.Whitelist)
	}
	waitFor(t, "tunnel online", func() bool { return e.tunnel(t, created.ID).Metrics.Status == "online" })
	if _, err := echoVia(port, "x"); err == nil {
		t.Fatal("link-only tunnel must block clients without authorisation")
	}

	status, body = e.call(t, "POST", "/api/tunnels/"+created.ID+"/invites", map[string]any{"note": "张三", "lease_duration": "8h", "require_approval": true})
	if status != http.StatusCreated {
		t.Fatalf("create invite: %d %s", status, body)
	}
	var out struct {
		Invite    protocol.Invite `json:"invite"`
		Link      string          `json:"link"`
		ShareText string          `json:"share_text"`
	}
	_ = json.Unmarshal(body, &out)
	if out.Invite.MaxUses != 1 || time.Until(out.Invite.ExpiresAt) < 23*time.Hour || out.Invite.LeaseDuration != "8h" || !out.Invite.RequireApproval {
		t.Fatalf("default invite should be 24h / 1 device: %+v", out.Invite)
	}
	for _, want := range []string{"给 张三", fmt.Sprintf("127.0.0.1:%d", port), "管理员批准", "8 小时", out.Link, "仅限 1 台设备"} {
		if !strings.Contains(out.ShareText, want) {
			t.Errorf("share text missing %q:\n%s", want, out.ShareText)
		}
	}
	waitFor(t, "invite listed", func() bool { return len(e.tunnel(t, created.ID).Metrics.Invites) == 1 })
	if status, _ := e.call(t, "GET", "/api/tunnels/"+created.ID+"/invites/"+out.Invite.Code+"/share", nil); status != http.StatusOK {
		t.Fatalf("share text for existing invite: %d", status)
	}

	// 同事打开链接提交申请
	resp, err := http.PostForm(fmt.Sprintf("http://%s/auth", e.addr), url.Values{"code": {out.Invite.Code}, "name": {"张三"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var pending protocol.AccessRequest
	waitFor(t, "pending request listed", func() bool {
		p := e.tunnel(t, created.ID).Metrics.Pending
		if len(p) == 1 {
			pending = p[0]
		}
		return len(p) == 1
	})
	if _, err := echoVia(port, "x"); err == nil {
		t.Fatal("pending request must not grant access")
	}

	status, body = e.call(t, "POST", "/api/tunnels/"+created.ID+"/requests/"+pending.ID, map[string]bool{"approve": true})
	if status != http.StatusOK {
		t.Fatalf("approve: %d %s", status, body)
	}
	waitFor(t, "lease granted after approval", func() bool {
		l := e.tunnel(t, created.ID).Metrics.DynamicLeases
		return len(l) == 1 && l[0].Name == "张三" && time.Until(l[0].ExpiresAt) > 7*time.Hour
	})
	if got, err := echoVia(port, "hi"); err != nil || got != "hi" {
		t.Fatalf("approved client cannot use tunnel: %q %v", got, err)
	}

	// 作废邀请后放通被收回
	if status, _ := e.call(t, "DELETE", "/api/tunnels/"+created.ID+"/invites/"+out.Invite.Code, nil); status != http.StatusOK {
		t.Fatal("revoke invite failed")
	}
	waitFor(t, "lease removed with invite", func() bool { return len(e.tunnel(t, created.ID).Metrics.DynamicLeases) == 0 })
}

func TestLinkOnlyWhitelistSerializesAsEmptyArray(t *testing.T) {
	cm := newTestConfig(t, "127.0.0.1:1", testToken)
	if _, err := cm.AddTunnel(protocol.TunnelConfig{Name: "a", Target: "10.0.0.1:22", ListenPort: 33703}); err != nil {
		t.Fatal(err)
	}
	h := NewAPIServer(cm, NewAgent(cm), nil).Handler()
	req := httptest.NewRequest("GET", "/api/tunnels", nil)
	req.Host = "127.0.0.1:33080"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"whitelist":[]`) {
		t.Fatalf("link-only whitelist must be [] (not null): %s", rec.Body.String())
	}
}

func TestIdleAutoClose(t *testing.T) {
	// 闲置单位缩短为 3 秒，便于测试（配置里的“1 分钟”即 3 秒）
	e := startEnvWith(t, testToken, func(a *Agent) { a.idleUnit = 3 * time.Second })
	waitFor(t, "agent connected", e.agent.IsConnected)

	// 1) 没有任何连接/授权/邀请：闲置到期后自动关闭并记录原因
	port := freeRelayPort(t)
	_, body := e.call(t, "POST", "/api/tunnels", map[string]any{"name": "idle", "target": "127.0.0.1:1", "listen_port": port, "idle_close_minutes": 1})
	var idle protocol.TunnelConfig
	_ = json.Unmarshal(body, &idle)
	waitFor(t, "idle tunnel closed", func() bool { return !e.tunnel(t, idle.ID).Enabled })
	if got := e.tunnel(t, idle.ID).CloseReason; !strings.Contains(got, "闲置") {
		t.Fatalf("close reason should mention idle, got %q", got)
	}

	// 2) 有可用的邀请链接时保持开启；作废后闲置关闭
	port2 := freeRelayPort(t, port)
	_, body = e.call(t, "POST", "/api/tunnels", map[string]any{"name": "invited", "target": "127.0.0.1:1", "listen_port": port2, "idle_close_minutes": 1})
	var inv protocol.TunnelConfig
	_ = json.Unmarshal(body, &inv)
	waitFor(t, "tunnel online", func() bool { return e.tunnel(t, inv.ID).Metrics.Status == "online" })
	_, ib := e.call(t, "POST", "/api/tunnels/"+inv.ID+"/invites", map[string]any{})
	var ir struct {
		Invite protocol.Invite `json:"invite"`
	}
	_ = json.Unmarshal(ib, &ir)
	time.Sleep(6 * time.Second)
	if !e.tunnel(t, inv.ID).Enabled {
		t.Fatal("tunnel with an unused invite must stay open")
	}
	if act := e.tunnel(t, inv.ID).Activity; act == nil || !act.InUse {
		t.Fatalf("tunnel with an unused invite should be reported in use: %+v", act)
	}
	e.call(t, "DELETE", "/api/tunnels/"+inv.ID+"/invites/"+ir.Invite.Code, nil)
	waitFor(t, "tunnel closed after invite revoked", func() bool { return !e.tunnel(t, inv.ID).Enabled })

	// 3) 不自动关闭（0）
	port3 := freeRelayPort(t, port, port2)
	_, body = e.call(t, "POST", "/api/tunnels", map[string]any{"name": "keep", "target": "127.0.0.1:1", "listen_port": port3, "idle_close_minutes": 0})
	var keep protocol.TunnelConfig
	_ = json.Unmarshal(body, &keep)
	time.Sleep(6 * time.Second)
	if !e.tunnel(t, keep.ID).Enabled {
		t.Fatal("idle_close_minutes=0 must never auto-close")
	}
}

func TestFirstConnectionAndConnectedTime(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)
	target := startEcho(t)
	port := freeRelayPort(t)
	_, body := e.call(t, "POST", "/api/tunnels", map[string]any{"name": "db", "target": target, "listen_port": port, "whitelist": []string{"*"}})
	var tun protocol.TunnelConfig
	_ = json.Unmarshal(body, &tun)
	waitFor(t, "tunnel online", func() bool { return e.tunnel(t, tun.ID).Metrics.Status == "online" })
	if e.tunnel(t, tun.ID).FirstConnectedAt != nil {
		t.Fatal("no connection yet")
	}

	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first connection recorded", func() bool { return e.tunnel(t, tun.ID).FirstConnectedAt != nil })
	if rh := e.tunnel(t, tun.ID).RateHistory; rh == nil || len(rh.Up) != ratePoints || len(rh.Down) != ratePoints {
		t.Fatalf("enabled tunnel should expose a %d-point rate history: %+v", ratePoints, rh)
	}
	time.Sleep(3 * time.Second)
	act := e.tunnel(t, tun.ID).Activity
	if act == nil || act.ConnectedSec < 2 || !act.InUse {
		t.Fatalf("connected time should accumulate while a client is connected: %+v", act)
	}
	c.Close()
	time.Sleep(2 * time.Second)
	before := e.tunnel(t, tun.ID).Activity.ConnectedSec
	time.Sleep(2 * time.Second)
	if after := e.tunnel(t, tun.ID).Activity.ConnectedSec; after != before {
		t.Fatalf("connected time must not grow without connections: %d -> %d", before, after)
	}

	e.call(t, "POST", "/api/tunnels/"+tun.ID+"/toggle", map[string]bool{"enabled": false})
	got := e.tunnel(t, tun.ID)
	if got.LastConnectedSec < 2 || got.CloseReason != "手动关闭" {
		t.Fatalf("stop should keep last connected time and reason: sec=%d reason=%q", got.LastConnectedSec, got.CloseReason)
	}
	// 重新开启：本次统计清零
	e.call(t, "POST", "/api/tunnels/"+tun.ID+"/toggle", map[string]bool{"enabled": true})
	if got := e.tunnel(t, tun.ID); got.FirstConnectedAt != nil || got.ConnectedSec != 0 || got.CloseReason != "" {
		t.Fatalf("new session should reset counters: %+v", got)
	}
}

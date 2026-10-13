package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/DeepFal/PortRelay/core/protocol"
)

const testToken = "secret"

type peekedConn struct {
	net.Conn
	r io.Reader
}

func (c *peekedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// startServer 复刻 cmd/portrelay-server 的分流逻辑
func startServer(t *testing.T, m *Manager) string {
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
			go func(c net.Conn) {
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				br := bufio.NewReader(c)
				p, err := br.Peek(3)
				if err != nil {
					c.Close()
					return
				}
				_ = c.SetReadDeadline(time.Time{})
				if s := string(p); s == "GET" || s == "POS" || s == "HEA" {
					m.HandleHTTP(&peekedConn{c, br})
					return
				}
				_ = m.HandleSession(&peekedConn{c, br})
			}(c)
		}
	}()
	return ln.Addr().String()
}

type testAgent struct {
	sess *yamux.Session
	w    *bufio.Writer
	msgs chan []byte
}

// dialAgent 连接服务端、发送鉴权，并对每条数据流回复 "OK"
func dialAgent(t *testing.T, addr, token string) *testAgent {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	s, err := yamux.Client(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	a := &testAgent{sess: s, w: bufio.NewWriter(cs), msgs: make(chan []byte, 64)}
	t.Cleanup(func() { s.Close() })
	go func() {
		r := bufio.NewReader(cs)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				close(a.msgs)
				return
			}
			var base protocol.BaseMessage
			if json.Unmarshal(line, &base) == nil && base.Type != protocol.TypeMetrics {
				a.msgs <- line
			}
		}
	}()
	go func() {
		for {
			st, err := s.AcceptStream()
			if err != nil {
				return
			}
			go func() {
				// 回复 OK 后保持连接，直到对端关闭（便于验证服务端主动断开）
				r := bufio.NewReader(st)
				_, _ = r.ReadBytes('\n')
				_, _ = st.Write([]byte("OK"))
				_, _ = io.Copy(io.Discard, r)
				st.Close()
			}()
		}
	}()
	a.send(t, protocol.AuthMessage{Type: protocol.TypeAuth, Token: token})
	return a
}

func (a *testAgent) send(t *testing.T, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	_, _ = a.w.Write(append(b, '\n'))
	if err := a.w.Flush(); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func (a *testAgent) next(t *testing.T) []byte {
	t.Helper()
	select {
	case m, ok := <-a.msgs:
		if !ok {
			return nil
		}
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for control message")
		return nil
	}
}

func (a *testAgent) expectAuth(t *testing.T) protocol.AuthRespMessage {
	t.Helper()
	var resp protocol.AuthRespMessage
	if line := a.next(t); line != nil {
		_ = json.Unmarshal(line, &resp)
	}
	return resp
}

func (a *testAgent) register(t *testing.T, msgType string, cfg protocol.TunnelConfig) protocol.RegisterRespMessage {
	t.Helper()
	if msgType == protocol.TypeUpdateTunnel {
		a.send(t, protocol.UpdateTunnelMessage{Type: msgType, Tunnel: cfg})
	} else {
		a.send(t, protocol.RegisterTunnelMessage{Type: msgType, Tunnel: cfg})
	}
	var resp protocol.RegisterRespMessage
	_ = json.Unmarshal(a.next(t), &resp)
	return resp
}

// createInvite asks the server for an invite through the control stream
func (a *testAgent) createInvite(t *testing.T, tunnelID string, maxUses int) protocol.Invite {
	t.Helper()
	return a.createInviteWith(t, tunnelID, maxUses, "")
}

func (a *testAgent) createInviteWith(t *testing.T, tunnelID string, maxUses int, lease string) protocol.Invite {
	t.Helper()
	return a.createInviteFull(t, tunnelID, maxUses, lease, false)
}

func (a *testAgent) createInviteFull(t *testing.T, tunnelID string, maxUses int, lease string, approval bool) protocol.Invite {
	t.Helper()
	a.send(t, protocol.CreateInviteMessage{Type: protocol.TypeCreateInvite, RequestID: "r1", TunnelID: tunnelID, ValidMinutes: 60, MaxUses: maxUses, LeaseDuration: lease, RequireApproval: approval})
	var resp protocol.InviteRespMessage
	_ = json.Unmarshal(a.next(t), &resp)
	if !resp.Success {
		t.Fatalf("create invite failed: %s", resp.Error)
	}
	return resp.Invite
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func postForm(t *testing.T, addr, code, name string) (int, string, string) {
	t.Helper()
	resp, err := noRedirect.PostForm("http://"+addr+"/auth", url.Values{"code": {code}, "name": {name}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(b)
}

// freePort picks a port outside the OS ephemeral range (so outgoing connections cannot
// grab it between the check and the tunnel's bind) and verifies it is bindable on 0.0.0.0.
func freePort(t *testing.T) int {
	t.Helper()
	start := 20000 + rand.Intn(9000)
	for i := 0; i < 1000; i++ {
		p := 20000 + (start-20000+i)%9000
		if ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", p)); err == nil {
			ln.Close()
			return p
		}
	}
	t.Fatal("no free port")
	return 0
}

func newTestManager() *Manager {
	return NewManager(1024, 65535, testToken)
}

func tunnelCfg(id string, port int) protocol.TunnelConfig {
	return protocol.TunnelConfig{ID: id, Name: id, ListenPort: port, Whitelist: []string{"*"}, Enabled: true, AuthCode: "code-" + id}
}

func hit(port int) string {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		return "dial error"
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	b := make([]byte, 2)
	n, _ := io.ReadFull(c, b)
	return string(b[:n])
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestWrongTokenIsRejected(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, "WRONG")
	if resp := a.expectAuth(t); resp.Success {
		t.Fatal("auth with wrong token succeeded")
	}
	port := freePort(t)
	// 服务端在鉴权失败后会关闭连接，这里的写入允许失败
	b, _ := json.Marshal(protocol.RegisterTunnelMessage{Type: protocol.TypeRegisterTunnel, Tunnel: tunnelCfg("evil", port)})
	_, _ = a.w.Write(append(b, '\n'))
	_ = a.w.Flush()
	time.Sleep(200 * time.Millisecond)
	if got := hit(port); got == "OK" {
		t.Fatal("unauthenticated agent managed to open a tunnel")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.tunnels) != 0 {
		t.Fatalf("expected no tunnels, got %d", len(m.tunnels))
	}
}

func TestPortProbeDoesNotBreakActiveSession(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	if !a.expectAuth(t).Success {
		t.Fatal("auth failed")
	}

	// 模拟 nc -z、端口扫描、非 HTTP 垃圾数据
	c, _ := net.Dial("tcp", addr)
	c.Close()
	c, _ = net.Dial("tcp", addr)
	_, _ = c.Write([]byte("SSH-2.0-scanner\r\n"))
	c.Close()
	time.Sleep(200 * time.Millisecond)

	port := freePort(t)
	if resp := a.register(t, protocol.TypeRegisterTunnel, tunnelCfg("b", port)); !resp.Success {
		t.Fatalf("register failed: %s", resp.Error)
	}
	if got := hit(port); got != "OK" {
		t.Fatalf("tunnel registered after port probe is broken: got %q", got)
	}
}

func TestStaleSessionTeardownKeepsNewSessionTunnels(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)

	old := dialAgent(t, addr, testToken)
	if !old.expectAuth(t).Success {
		t.Fatal("auth failed")
	}
	port := freePort(t)
	if resp := old.register(t, protocol.TypeRegisterTunnel, tunnelCfg("a", port)); !resp.Success {
		t.Fatal(resp.Error)
	}

	// 唤醒后 Agent 立刻重连并重新注册，旧会话随后才断开
	fresh := dialAgent(t, addr, testToken)
	if !fresh.expectAuth(t).Success {
		t.Fatal("auth failed")
	}
	if resp := fresh.register(t, protocol.TypeRegisterTunnel, tunnelCfg("a", port)); !resp.Success {
		t.Fatalf("re-register on new session failed: %s", resp.Error)
	}
	old.sess.Close()
	time.Sleep(300 * time.Millisecond)

	if got := hit(port); got != "OK" {
		t.Fatalf("tunnel lost after stale session teardown: got %q", got)
	}
}

func TestUpdateKeepsConnectionsAndLeases(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)

	port := freePort(t)
	cfg := tunnelCfg("a", port)
	a.register(t, protocol.TypeRegisterTunnel, cfg)

	m.mu.RLock()
	tun := m.tunnels["a"]
	m.mu.RUnlock()
	tun.AddDynamicWhitelist("10.1.2.3", time.Now().Add(time.Hour))

	cfg.Name = "renamed"
	cfg.Whitelist = []string{"127.0.0.1"}
	if resp := a.register(t, protocol.TypeUpdateTunnel, cfg); !resp.Success {
		t.Fatal(resp.Error)
	}
	m.mu.RLock()
	same := m.tunnels["a"] == tun
	m.mu.RUnlock()
	if !same {
		t.Fatal("metadata update recreated the tunnel (would drop active connections)")
	}
	if got := tun.GetDynamicWhitelist(); len(got) != 1 || got[0] != "10.1.2.3" {
		t.Fatalf("dynamic lease lost after update: %v", got)
	}
	if hit(port) != "OK" {
		t.Fatal("tunnel unusable after update")
	}
}

func TestUpdatePortConflictKeepsOldTunnel(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)

	p1, p2 := freePort(t), freePort(t)
	a.register(t, protocol.TypeRegisterTunnel, tunnelCfg("a", p1))
	a.register(t, protocol.TypeRegisterTunnel, tunnelCfg("b", p2))

	moved := tunnelCfg("a", p2)
	if resp := a.register(t, protocol.TypeUpdateTunnel, moved); resp.Success {
		t.Fatal("moving onto an occupied port should fail")
	}
	if hit(p1) != "OK" {
		t.Fatal("failed port change destroyed the original tunnel")
	}

	p3 := freePort(t)
	if resp := a.register(t, protocol.TypeUpdateTunnel, tunnelCfg("a", p3)); !resp.Success {
		t.Fatal(resp.Error)
	}
	if hit(p3) != "OK" {
		t.Fatal("tunnel not reachable on new port")
	}
	if hit(p1) == "OK" {
		t.Fatal("old port still served after move")
	}
}

func TestUpdateOfUnknownTunnelRegistersIt(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	port := freePort(t)
	if resp := a.register(t, protocol.TypeUpdateTunnel, tunnelCfg("x", port)); !resp.Success {
		t.Fatal(resp.Error)
	}
	if hit(port) != "OK" {
		t.Fatal("update of unknown tunnel did not register it")
	}
}

func TestDisconnectCleansUpTunnels(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	port := freePort(t)
	a.register(t, protocol.TypeRegisterTunnel, tunnelCfg("a", port))
	a.sess.Close()
	waitFor(t, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.active == nil && len(m.tunnels) == 0
	})
}

func httpGet(t *testing.T, addr, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestInviteFlowIgnoresForwardedHeadersAndEscapesHTML(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	cfg.Name = `<script>alert(1)</script>`
	cfg.Whitelist = nil // 仅授权链接
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	inv := a.createInvite(t, "a", 1)

	if hit(cfg.ListenPort) == "OK" {
		t.Fatal("link-only tunnel must reject unauthorised clients")
	}

	status, body := httpGet(t, addr, "/i/"+inv.Code, map[string]string{"X-Forwarded-For": "8.8.8.8"})
	if status != 200 || !strings.Contains(body, `name="name"`) {
		t.Fatalf("invite form not shown: %d", status)
	}
	if strings.Contains(body, "<script>") {
		t.Fatal("tunnel name rendered without escaping")
	}

	status, _, body = postForm(t, addr, inv.Code, `<b>张三</b>`)
	if status != 200 || !strings.Contains(body, "授权成功") {
		t.Fatalf("submit failed: %d %s", status, body)
	}
	if strings.Contains(body, "<b>张三</b>") {
		t.Fatal("name rendered without escaping")
	}
	leases := m.state.leaseList("a")
	if len(leases) != 1 || leases[0].IP != "127.0.0.1" || leases[0].Name != "<b>张三</b>" || leases[0].Invite != inv.Code {
		t.Fatalf("unexpected leases (X-Forwarded-For must be ignored): %+v", leases)
	}
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("authorised client cannot connect")
	}

	// 同一设备再次打开同一链接不占用新名额
	if status, _, _ := postForm(t, addr, inv.Code, "张三"); status != 200 {
		t.Fatalf("same device reuse rejected: %d", status)
	}
	if uses := m.state.inviteList("a")[0].Uses; len(uses) != 1 {
		t.Fatalf("reuse by same device consumed another slot: %+v", uses)
	}

	if status, _ := httpGet(t, addr, "/i/wrongcode", nil); status != 404 {
		t.Fatalf("wrong code: status %d", status)
	}
	if status, _, _ := postForm(t, addr, inv.Code, "   "); status != 400 {
		t.Fatalf("empty name accepted: %d", status)
	}
}

func TestLegacyFixedAuthCodeNoLongerWorks(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	if status, _ := httpGet(t, addr, "/auth?code="+cfg.AuthCode, nil); status != 404 {
		t.Fatalf("legacy fixed code should be rejected, got %d", status)
	}
}

func TestApprovalFlow(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	cfg.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	inv := a.createInviteFull(t, "a", 1, "", true)

	status, loc, _ := postForm(t, addr, inv.Code, "李四")
	if status != http.StatusSeeOther || !strings.HasPrefix(loc, "/auth/status?id=") {
		t.Fatalf("expected redirect to status page, got %d %q", status, loc)
	}
	if _, body := httpGet(t, addr, loc, nil); !strings.Contains(body, "等待管理员批准") {
		t.Fatal("status page should show pending")
	}
	if hit(cfg.ListenPort) == "OK" {
		t.Fatal("pending request must not grant access")
	}
	pending := m.state.pendingList("a")
	if len(pending) != 1 || pending[0].Name != "李四" {
		t.Fatalf("pending list: %+v", pending)
	}

	a.send(t, protocol.DecideRequestMessage{Type: protocol.TypeDecideRequest, TunnelID: "a", RequestID: pending[0].ID, Approve: true})
	waitFor(t, func() bool { return len(m.state.leaseList("a")) == 1 })
	if _, body := httpGet(t, addr, loc, nil); !strings.Contains(body, "申请已批准") {
		t.Fatal("status page should show approval")
	}
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("approved client cannot connect")
	}

	// 拒绝流程
	inv2 := a.createInviteFull(t, "a", 1, "", true)
	m.state.removeLease("a", "")
	_, loc2, _ := postForm(t, addr, inv2.Code, "王五")
	id := strings.TrimPrefix(loc2, "/auth/status?id=")
	a.send(t, protocol.DecideRequestMessage{Type: protocol.TypeDecideRequest, TunnelID: "a", RequestID: id, Approve: false})
	waitFor(t, func() bool {
		r, ok := m.state.request(id)
		return ok && r.Status == "rejected"
	})
	if _, body := httpGet(t, addr, loc2, nil); !strings.Contains(body, "申请未通过") {
		t.Fatal("status page should show rejection")
	}
}

func TestRevokeInviteRemovesItsLeases(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	cfg.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	inv := a.createInvite(t, "a", 0)
	postForm(t, addr, inv.Code, "张三")
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("grant failed")
	}
	a.send(t, protocol.RevokeInviteMessage{Type: protocol.TypeRevokeInvite, TunnelID: "a", Code: inv.Code})
	waitFor(t, func() bool { return len(m.state.leaseList("a")) == 0 && len(m.state.inviteList("a")) == 0 })
	if hit(cfg.ListenPort) == "OK" {
		t.Fatal("leases of revoked invite still active")
	}
	if status, _ := httpGet(t, addr, "/i/"+inv.Code, nil); status != 404 {
		t.Fatal("revoked invite still usable")
	}
}

func TestApprovalIsPerInvite(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	cfg.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, cfg)

	// 同一通道：需要审批的邀请进入待审批，不需要审批的直接放通
	needs := a.createInviteFull(t, "a", 1, "", true)
	direct := a.createInviteFull(t, "a", 1, "", false)
	if status, loc, _ := postForm(t, addr, needs.Code, "甲"); status != http.StatusSeeOther || !strings.HasPrefix(loc, "/auth/status") {
		t.Fatalf("approval invite should go to pending, got %d %q", status, loc)
	}
	if hit(cfg.ListenPort) == "OK" {
		t.Fatal("pending request granted access")
	}
	m.state.removeLease("a", "")
	if status, _, body := postForm(t, addr, direct.Code, "乙"); status != 200 || !strings.Contains(body, "授权成功") {
		t.Fatalf("direct invite should grant immediately, got %d", status)
	}
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("direct invite did not grant access")
	}
}

func TestGrantAfterRevokeAllDoesNotPanic(t *testing.T) {
	s := newStateStore()
	inv := s.createInvite("a", "", time.Hour, 0, "", false)
	if _, err := s.useInviteAndGrant("a", inv.Code, "甲", "10.0.0.1", nil); err != nil {
		t.Fatal(err)
	}
	s.removeLease("a", "") // 全部撤销
	if _, err := s.useInviteAndGrant("a", inv.Code, "乙", "10.0.0.2", nil); err != nil {
		t.Fatal(err)
	}
	if l := s.leaseList("a"); len(l) != 1 || l[0].IP != "10.0.0.2" {
		t.Fatalf("grant after revoke-all failed: %+v", l)
	}
}

func TestExpiredLeaseDisconnectsActiveConnection(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	cfg.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, cfg)

	m.mu.RLock()
	tun := m.tunnels["a"]
	m.mu.RUnlock()
	tun.AddDynamicWhitelist("127.0.0.1", time.Now().Add(1500*time.Millisecond))

	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.ListenPort))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	buf := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("leased client should connect: %v", err)
	}

	// 授权到期后，连接应在下一次检查（约 1 秒）内被断开
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	start := time.Now()
	_, err = c.Read(buf)
	if err == nil {
		t.Fatal("connection still alive after lease expired")
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection was not closed within %v of lease expiry", time.Since(start))
	}
}

func TestLeaseCountdownStartsAtFirstConnection(t *testing.T) {
	s := newStateStore()
	now := time.Now()
	inv := s.createInvite("a", "", time.Hour, 1, protocol.LeaseDuration2h, false)
	l, err := s.useInviteAndGrant("a", inv.Code, "甲", "10.0.0.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Deferred || l.StartedAt != nil || l.ExpiresAt.Sub(now) < 23*time.Hour {
		t.Fatalf("new lease should wait for first connection within 24h: %+v", l)
	}
	// 1 小时后才首次连接：从那时起算 2 小时
	later := now.Add(time.Hour)
	exp, started := s.startLease("a", "10.0.0.1", nil, later)
	if !started || !exp.Equal(later.Add(2*time.Hour)) {
		t.Fatalf("countdown should start at first connection: started=%v exp=%v", started, exp)
	}
	// 再次连接不会重置计时
	if _, again := s.startLease("a", "10.0.0.1", nil, later.Add(30*time.Minute)); again {
		t.Fatal("countdown must only start once")
	}
	// 通道开启期间有效：立即开始，无需等待
	inv2 := s.createInvite("a", "", time.Hour, 1, protocol.LeaseDurationTunnel, false)
	l2, _ := s.useInviteAndGrant("a", inv2.Code, "乙", "10.0.0.2", nil)
	if l2.Deferred {
		t.Fatal("tunnel-lifetime lease should start immediately")
	}
	// 首次连接截止时间不超过通道自身到期时间
	tunnelEnd := now.Add(3 * time.Hour)
	inv3 := s.createInvite("a", "", time.Hour, 1, "", false)
	l3, _ := s.useInviteAndGrant("a", inv3.Code, "丙", "10.0.0.3", &tunnelEnd)
	if !l3.ExpiresAt.Equal(tunnelEnd) {
		t.Fatalf("start window should be capped by tunnel TTL: %v", l3.ExpiresAt)
	}
}

func TestInviteCapacityAndExpiry(t *testing.T) {
	s := newStateStore()
	inv := s.createInvite("a", "", time.Hour, 1, "", false)
	if _, err := s.useInviteAndGrant("a", inv.Code, "甲", "10.0.0.1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.useInviteAndGrant("a", inv.Code, "甲", "10.0.0.1", nil); err != nil {
		t.Fatalf("same device should be able to reuse: %v", err)
	}
	if _, err := s.useInviteAndGrant("a", inv.Code, "乙", "10.0.0.2", nil); err != errInviteUsedUp {
		t.Fatalf("second device should be rejected, got %v", err)
	}
	// 待审批的申请也占用名额
	inv2 := s.createInvite("a", "", time.Hour, 1, "", false)
	if _, err := s.addRequest("a", inv2.Code, "丙", "10.0.0.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.addRequest("a", inv2.Code, "丁", "10.0.0.4"); err != errInviteUsedUp {
		t.Fatalf("pending request should hold the slot, got %v", err)
	}

	expired := s.createInvite("a", "", -time.Minute, 0, "", false)
	if _, _, ok := s.findInvite(expired.Code); ok {
		t.Fatal("expired invite must not be found")
	}
}

func TestLeaseExpiryModes(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	cases := map[string]time.Time{
		"":                           now.Add(2 * time.Hour),
		protocol.LeaseDuration1h:     now.Add(time.Hour),
		protocol.LeaseDuration8h:     now.Add(8 * time.Hour),
		protocol.LeaseDurationToday:  time.Date(2026, 10, 13, 23, 59, 59, 0, time.Local),
		protocol.LeaseDurationTunnel: now.Add(30 * 24 * time.Hour),
	}
	for mode, want := range cases {
		if got := leaseExpiry(mode, nil, now); !got.Equal(want) {
			t.Errorf("mode %q: got %v want %v", mode, got, want)
		}
	}
	// 不超过通道自身的到期时间
	tunnelEnd := now.Add(time.Hour)
	if got := leaseExpiry(protocol.LeaseDuration8h, &tunnelEnd, now); !got.Equal(tunnelEnd) {
		t.Errorf("lease outlives tunnel TTL: %v", got)
	}
}

func TestLeaseDurationComesFromInvite(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)

	// 直接放通：同一通道上两个邀请各自的时长
	cfg := tunnelCfg("a", freePort(t))
	cfg.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	short := a.createInviteWith(t, "a", 1, protocol.LeaseDuration1h)
	long := a.createInviteWith(t, "a", 1, protocol.LeaseDurationToday)
	if short.LeaseDuration != "1h" || long.LeaseDuration != "today" {
		t.Fatalf("invite lease durations not stored: %q %q", short.LeaseDuration, long.LeaseDuration)
	}
	if _, body := httpGet(t, addr, "/i/"+short.Code, nil); !strings.Contains(body, "1 小时") {
		t.Fatal("invite page should show the invite's own lease duration")
	}
	postForm(t, addr, short.Code, "张三")
	leases := m.state.leaseList("a")
	if len(leases) != 1 || !leases[0].AwaitingConnect || time.Until(leases[0].ExpiresAt) < 23*time.Hour {
		t.Fatalf("lease should wait for the first connection (24h window): %+v", leases)
	}
	// 首次连接后开始计时，按邀请的 1 小时计算
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("authorised client cannot connect")
	}
	waitFor(t, func() bool { l := m.state.leaseList("a"); return len(l) == 1 && !l[0].AwaitingConnect })
	leases = m.state.leaseList("a")
	if d := time.Until(leases[0].ExpiresAt); d > 61*time.Minute || d < 59*time.Minute || leases[0].StartedAt == nil {
		t.Fatalf("lease should last 1h from first connection: %+v", leases)
	}

	// 审批：批准后的时长同样取自邀请
	cfgB := tunnelCfg("b", freePort(t))
	cfgB.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, cfgB)
	inv := a.createInviteFull(t, "b", 1, protocol.LeaseDuration8h, true)
	postForm(t, addr, inv.Code, "李四")
	pending := m.state.pendingList("b")
	if len(pending) != 1 {
		t.Fatal("expected a pending request")
	}
	a.send(t, protocol.DecideRequestMessage{Type: protocol.TypeDecideRequest, TunnelID: "b", RequestID: pending[0].ID, Approve: true})
	waitFor(t, func() bool { return len(m.state.leaseList("b")) == 1 })
	if !m.state.leaseList("b")[0].AwaitingConnect {
		t.Fatal("approved lease should wait for the first connection")
	}
	if hit(cfgB.ListenPort) != "OK" {
		t.Fatal("approved client cannot connect")
	}
	waitFor(t, func() bool { return !m.state.leaseList("b")[0].AwaitingConnect })
	if d := time.Until(m.state.leaseList("b")[0].ExpiresAt); d < 7*time.Hour+50*time.Minute || d > 8*time.Hour+time.Minute {
		t.Fatalf("approved lease should last 8h from first connection, got %v", d)
	}

	// 无效时长被拒绝
	a.send(t, protocol.CreateInviteMessage{Type: protocol.TypeCreateInvite, RequestID: "bad", TunnelID: "a", ValidMinutes: 60, MaxUses: 1, LeaseDuration: "99y"})
	var resp protocol.InviteRespMessage
	_ = json.Unmarshal(a.next(t), &resp)
	if resp.Success {
		t.Fatal("invalid lease duration should be rejected")
	}
}

func TestStateSurvivesRestartAndLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	port := freePort(t)
	cfg := tunnelCfg("a", port)
	cfg.Whitelist = nil

	state := dir + "/state.json"
	m1 := newTestManager()
	if err := m1.EnableLeasePersistence(state); err != nil {
		t.Fatal(err)
	}
	addr1 := startServer(t, m1)
	a1 := dialAgent(t, addr1, testToken)
	a1.expectAuth(t)
	a1.register(t, protocol.TypeRegisterTunnel, cfg)
	inv := a1.createInvite(t, "a", 2)
	postForm(t, addr1, inv.Code, "张三")
	a1.sess.Close()
	waitFor(t, func() bool {
		m1.mu.RLock()
		defer m1.mu.RUnlock()
		return len(m1.tunnels) == 0
	})

	// 模拟服务端重启：新 Manager 读取同一状态文件，Agent 重新注册后放通与邀请仍然有效
	m2 := newTestManager()
	if err := m2.EnableLeasePersistence(state); err != nil {
		t.Fatal(err)
	}
	addr2 := startServer(t, m2)
	a2 := dialAgent(t, addr2, testToken)
	a2.expectAuth(t)
	if resp := a2.register(t, protocol.TypeRegisterTunnel, cfg); !resp.Success {
		t.Fatal(resp.Error)
	}
	if got := hit(port); got != "OK" {
		t.Fatalf("lease lost after restart: got %q", got)
	}
	if invs := m2.state.inviteList("a"); len(invs) != 1 || len(invs[0].Uses) != 1 || invs[0].Uses[0].Name != "张三" {
		t.Fatalf("invite lost after restart: %+v", invs)
	}

	// 主动关闭通道后放通与邀请都失效
	a2.send(t, protocol.UnregisterMessage{Type: protocol.TypeUnregister, TunnelID: "a"})
	a2.next(t)
	a2.register(t, protocol.TypeRegisterTunnel, cfg)
	if got := hit(port); got == "OK" {
		t.Fatal("lease should be dropped when the tunnel is explicitly closed")
	}
	if len(m2.state.inviteList("a")) != 0 {
		t.Fatal("invites should be dropped when the tunnel is explicitly closed")
	}

	// 兼容上一版的 leases.json 格式
	legacy := dir + "/legacy.json"
	exp := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	if err := os.WriteFile(legacy, []byte(`{"t1":{"10.0.0.9":"`+exp+`"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m3 := newTestManager()
	if err := m3.EnableLeasePersistence(legacy); err != nil {
		t.Fatal(err)
	}
	if l := m3.state.leaseList("t1"); len(l) != 1 || l[0].IP != "10.0.0.9" {
		t.Fatalf("legacy lease file not loaded: %+v", l)
	}
}

func TestAuthPageShowsHostUsedByVisitor(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	inv := a.createInvite(t, "a", 1)

	_, body := httpGet(t, addr, "/i/"+inv.Code, nil)
	if !strings.Contains(body, fmt.Sprintf("127.0.0.1:%d", cfg.ListenPort)) {
		t.Fatal("auth page should show the host the visitor used")
	}
	if strings.Contains(body, "192.168.") {
		t.Fatal("auth page must not show a hard-coded host")
	}
}

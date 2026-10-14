package server

import (
	"encoding/json"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeepFal/PortRelay/core/protocol"
)

// linkOnly registers a tunnel that only invited devices can reach
func linkOnly(t *testing.T, a *testAgent, id string) protocol.TunnelConfig {
	t.Helper()
	cfg := tunnelCfg(id, freePort(t))
	cfg.Whitelist = nil
	if resp := a.register(t, protocol.TypeRegisterTunnel, cfg); !resp.Success {
		t.Fatal(resp.Error)
	}
	return cfg
}

// grantVia uses an invite from 127.0.0.1 and connects once so the countdown starts
func grantVia(t *testing.T, m *Manager, addr string, cfg protocol.TunnelConfig, code string) {
	t.Helper()
	if status, _, body := postForm(t, addr, code, "张三"); status != 200 {
		t.Fatalf("grant: %d %s", status, body)
	}
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("granted device cannot connect")
	}
	waitFor(t, func() bool { l := m.state.leaseList(cfg.ID); return len(l) == 1 && !l[0].AwaitingConnect })
}

// expire moves a lease's end into the past by ago
func expire(m *Manager, scope, ip string, ago time.Duration) {
	m.state.mu.Lock()
	l := m.state.data[scope].Leases[ip]
	l.ExpiresAt = time.Now().Add(-ago)
	m.state.data[scope].Leases[ip] = l
	m.state.mu.Unlock()
	m.syncScope(scope)
}

// renew presses「申请延长」and returns the status page path
func renew(t *testing.T, addr, code string) string {
	t.Helper()
	resp, err := noRedirect.PostForm("http://"+addr+"/renew", url.Values{"code": {code}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/auth/status?id=") {
		t.Fatalf("renew: %d %s", resp.StatusCode, b)
	}
	return resp.Header.Get("Location")
}

func (a *testAgent) extend(t *testing.T, tunnelID, ip string, minutes int) protocol.ExtendLeaseRespMessage {
	t.Helper()
	a.send(t, protocol.ExtendLeaseMessage{Type: protocol.TypeExtendLease, RequestID: "e1", TunnelID: tunnelID, IP: ip, Minutes: minutes})
	var resp protocol.ExtendLeaseRespMessage
	_ = json.Unmarshal(a.next(t), &resp)
	return resp
}

func TestRenewAfterExpiry(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := linkOnly(t, a, "a")
	inv := a.createInviteWith(t, "a", 1, "30m")
	grantVia(t, m, addr, cfg, inv.Code)

	// 授权有效时再打开链接：显示剩余时间与「申请延长」，不会重新授权
	if _, body := httpGet(t, addr, "/i/"+inv.Code, nil); !strings.Contains(body, "访问已授权") || !strings.Contains(body, "申请延长") {
		t.Fatalf("running access page: %s", body)
	}

	expire(m, "a", "127.0.0.1", time.Minute)
	waitFor(t, func() bool { return hit(cfg.ListenPort) != "OK" })
	if _, body := httpGet(t, addr, "/i/"+inv.Code, nil); !strings.Contains(body, "访问已结束") || !strings.Contains(body, "申请延长") {
		t.Fatalf("ended access page: %s", body)
	}
	// 用过链接的设备不能再直接授权
	if status, _, _ := postForm(t, addr, inv.Code, "张三"); status != 409 {
		t.Fatalf("a returning device must not be re-granted directly, got %d", status)
	}

	status := renew(t, addr, inv.Code)
	if _, body := httpGet(t, addr, status, nil); !strings.Contains(body, "等待管理员批准") {
		t.Fatalf("pending renewal page: %s", body)
	}
	if again := renew(t, addr, inv.Code); again != status {
		t.Fatal("one device should have one pending renewal")
	}
	rl := m.state.renewalList()
	if len(rl) != 1 || rl[0].Request == nil || rl[0].EndedAt == nil || rl[0].TunnelID != "a" || rl[0].Name != "张三" {
		t.Fatalf("renewal list: %+v", rl)
	}
	if p := m.state.pendingList("a"); len(p) != 0 {
		t.Fatalf("renewals are not first-time requests: %+v", p)
	}

	resp := a.extend(t, "a", "127.0.0.1", 45)
	if !resp.Success || time.Until(resp.ExpiresAt) < 44*time.Minute || time.Until(resp.ExpiresAt) > 46*time.Minute {
		t.Fatalf("extend an ended lease from now: %+v", resp)
	}
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("extended device cannot connect")
	}
	if _, body := httpGet(t, addr, status, nil); !strings.Contains(body, "延长已批准") {
		t.Fatalf("approved renewal page: %s", body)
	}
	if rl := m.state.renewalList(); len(rl) != 0 {
		t.Fatalf("nothing left to renew: %+v", rl)
	}
}

func TestRenewAfterTunnelClosed(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := linkOnly(t, a, "a")
	inv := a.createInvite(t, "a", 1)
	grantVia(t, m, addr, cfg, inv.Code)

	a.send(t, protocol.UnregisterMessage{Type: protocol.TypeUnregister, TunnelID: "a"})
	a.next(t)
	_, body := httpGet(t, addr, "/i/"+inv.Code, nil)
	if !strings.Contains(body, "访问已结束") || !strings.Contains(body, "通道当前已关闭") || !strings.Contains(body, "目标通道") {
		t.Fatalf("closed tunnel page: %s", body)
	}
	status := renew(t, addr, inv.Code)
	if _, body := httpGet(t, addr, status, nil); !strings.Contains(body, "等待管理员批准") || !strings.Contains(body, "自动重新开启") {
		t.Fatalf("pending renewal on a closed tunnel: %s", body)
	}
	// 拒绝也不需要通道开启
	a.send(t, protocol.DecideRequestMessage{Type: protocol.TypeDecideRequest, TunnelID: "a", RequestID: m.state.renewalList()[0].Request.ID, Approve: false})
	waitFor(t, func() bool {
		_, b := httpGet(t, addr, status, nil)
		return strings.Contains(b, "拒绝了您的延长申请")
	})

	// 再申请一次，批准后 Agent 重新开启通道，授权随注册生效
	status = renew(t, addr, inv.Code)
	if resp := a.extend(t, "a", "127.0.0.1", 60); !resp.Success {
		t.Fatalf("extend while closed: %s", resp.Error)
	}
	if _, body := httpGet(t, addr, status, nil); !strings.Contains(body, "正在开启通道") {
		t.Fatalf("approved while closed: %s", body)
	}
	if resp := a.register(t, protocol.TypeRegisterTunnel, cfg); !resp.Success {
		t.Fatal(resp.Error)
	}
	if hit(cfg.ListenPort) != "OK" {
		t.Fatal("the reopened tunnel must admit the extended device")
	}
}

func TestRenewWindowRevokeAndDelete(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := linkOnly(t, a, "a")
	inv := a.createInvite(t, "a", 0)
	grantVia(t, m, addr, cfg, inv.Code)

	// 结束超过 2 小时：只能重新邀请
	expire(m, "a", "127.0.0.1", renewWindow+time.Minute)
	if _, body := httpGet(t, addr, "/i/"+inv.Code, nil); !strings.Contains(body, "超过 2 小时") {
		t.Fatalf("expired window page: %s", body)
	}
	if resp := a.extend(t, "a", "127.0.0.1", 30); resp.Success {
		t.Fatal("cannot extend after the renewal window")
	}

	// 撤销的授权不能延长
	grantTestLease(m, "a", "10.9.9.9", time.Now().Add(time.Hour))
	a.send(t, protocol.RevokeLeaseMessage{Type: protocol.TypeRevokeLease, TunnelID: "a", IP: "10.9.9.9"})
	waitFor(t, func() bool { return len(m.state.leaseList("a")) == 0 })
	if resp := a.extend(t, "a", "10.9.9.9", 30); resp.Success || !strings.Contains(resp.Error, "撤销") {
		t.Fatalf("revoked access must not be extended: %+v", resp)
	}

	// 删除通道：连同记录一起清除
	grantTestLease(m, "a", "10.9.9.8", time.Now().Add(time.Hour))
	a.send(t, protocol.UnregisterMessage{Type: protocol.TypeUnregister, TunnelID: "a", Deleted: true})
	a.next(t)
	if rl := m.state.renewalList(); len(rl) != 0 {
		t.Fatalf("a deleted tunnel leaves nothing to renew: %+v", rl)
	}
	if resp := a.extend(t, "a", "10.9.9.8", 30); resp.Success {
		t.Fatal("cannot extend on a deleted tunnel")
	}
}

func TestExtendRunningLeaseIsNotCappedByTunnelTTL(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	cfg := tunnelCfg("a", freePort(t))
	cfg.Whitelist = nil
	end := time.Now().Add(10 * time.Minute)
	cfg.ExpiresAt = &end
	a.register(t, protocol.TypeRegisterTunnel, cfg)
	inv := a.createInvite(t, "a", 1)
	grantVia(t, m, addr, cfg, inv.Code)
	before := m.state.leaseList("a")[0].ExpiresAt
	if before.After(end.Add(time.Second)) {
		t.Fatalf("a regular grant is capped by the tunnel TTL: %v > %v", before, end)
	}

	resp := a.extend(t, "a", "127.0.0.1", 60)
	if !resp.Success || !resp.ExpiresAt.Equal(before.Add(time.Hour)) {
		t.Fatalf("a running lease extends from its expiry: %+v (was %v)", resp, before)
	}
}

func TestCustomLeaseDurations(t *testing.T) {
	for d, want := range map[string]int{"30m": 30, "90m": 90, "1h": 60, "": 120, "8h": 480} {
		if n, ok := protocol.LeaseMinutes(d); !ok || n != want {
			t.Errorf("LeaseMinutes(%q) = %d, %v", d, n, ok)
		}
	}
	for _, bad := range []string{"3m", "abc", "90", "100000m"} {
		if protocol.ValidLeaseDuration(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
	if got := protocol.DescribeLeaseDuration("90m"); got != "1 小时 30 分钟" {
		t.Errorf("describe 90m: %q", got)
	}
	now := time.Now()
	if got := leaseExpiry("45m", nil, now); !got.Equal(now.Add(45 * time.Minute)) {
		t.Errorf("leaseExpiry 45m: %v", got.Sub(now))
	}
}

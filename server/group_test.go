package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DeepFal/PortRelay/core/protocol"
)

// groupMember is a link-only tunnel that belongs to group "g"
func groupMember(id string, port int, target string) protocol.TunnelConfig {
	cfg := tunnelCfg(id, port)
	cfg.Whitelist = nil
	cfg.Target = target
	cfg.GroupID = "g"
	cfg.GroupName = "订单服务"
	return cfg
}

func (a *testAgent) createGroupInvite(t *testing.T, groupID string, lease string, approval bool) protocol.Invite {
	t.Helper()
	a.send(t, protocol.CreateInviteMessage{Type: protocol.TypeCreateInvite, RequestID: "g1", GroupID: groupID, ValidMinutes: 60, MaxUses: 1, LeaseDuration: lease, RequireApproval: approval})
	var resp protocol.InviteRespMessage
	_ = json.Unmarshal(a.next(t), &resp)
	if !resp.Success {
		t.Fatalf("create group invite failed: %s", resp.Error)
	}
	return resp.Invite
}

// setupGroup registers two members of group "g" plus one ungrouped link-only tunnel
func setupGroup(t *testing.T) (*Manager, *testAgent, string, [3]int) {
	t.Helper()
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	ports := [3]int{freePort(t), freePort(t), freePort(t)}
	a.register(t, protocol.TypeRegisterTunnel, groupMember("m1", ports[0], "10.0.1.11:22"))
	a.register(t, protocol.TypeRegisterTunnel, groupMember("m2", ports[1], "10.0.1.12:22"))
	other := tunnelCfg("other", ports[2])
	other.Whitelist = nil
	a.register(t, protocol.TypeRegisterTunnel, other)
	return m, a, addr, ports
}

// holdConn opens a client connection that stays up until the server closes it
func holdConn(t *testing.T, port int) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	buf := make([]byte, 2)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("connection to %d should be allowed: %v", port, err)
	}
	return c
}

func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection should have been closed")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection still open after access was revoked")
	}
}

func TestGroupInviteGrantsEveryMember(t *testing.T) {
	m, a, addr, p := setupGroup(t)
	if hit(p[0]) == "OK" {
		t.Fatal("link-only member must be closed before any grant")
	}
	inv := a.createGroupInvite(t, "g", "", false)

	status, body := httpGet(t, addr, "/i/"+inv.Code, nil)
	if status != 200 {
		t.Fatalf("invite page: %d %s", status, body)
	}
	for _, want := range []string{"目标通道组", "订单服务（2 个地址）", fmt.Sprintf("127.0.0.1:%d", p[0]), fmt.Sprintf("127.0.0.1:%d", p[1]), "10.0.1.11:22", "10.0.1.12:22"} {
		if !strings.Contains(body, want) {
			t.Errorf("invite page lacks %q", want)
		}
	}

	if status, _, body := postForm(t, addr, inv.Code, "张三"); status != 200 || !strings.Contains(body, "任意一个访问地址") {
		t.Fatalf("grant failed: %d %s", status, body)
	}
	if hit(p[0]) != "OK" || hit(p[1]) != "OK" {
		t.Fatal("a group grant must open every member")
	}
	if hit(p[2]) == "OK" {
		t.Fatal("a group grant must not open tunnels outside the group")
	}
	if l := m.state.leaseList(groupScope("g")); len(l) != 1 || l[0].Name != "张三" {
		t.Fatalf("group lease not recorded: %+v", l)
	}
	if l := m.state.leaseList("m1"); len(l) != 0 {
		t.Fatalf("group grant must not create per-tunnel leases: %+v", l)
	}
}

func TestGroupLeaseStartsOnFirstConnectionToAnyMember(t *testing.T) {
	m, a, addr, p := setupGroup(t)
	inv := a.createGroupInvite(t, "g", protocol.LeaseDuration2h, false)
	postForm(t, addr, inv.Code, "张三")
	if l := m.state.leaseList(groupScope("g")); len(l) != 1 || !l[0].AwaitingConnect {
		t.Fatalf("lease should wait for the first connection: %+v", l)
	}

	if hit(p[1]) != "OK" {
		t.Fatal("member 2 should be reachable")
	}
	waitFor(t, func() bool {
		l := m.state.leaseList(groupScope("g"))
		return len(l) == 1 && !l[0].AwaitingConnect
	})
	exp := m.state.leaseList(groupScope("g"))[0].ExpiresAt
	if d := time.Until(exp); d < 119*time.Minute || d > 121*time.Minute {
		t.Fatalf("countdown should be 2h from first connection, got %v", d)
	}
	// 其它成员的放通随之更新为同一到期时间
	if got := m.tunnel("m1").dynamicLeases()["127.0.0.1"]; !got.Equal(exp) {
		t.Fatalf("member 1 expiry %v, want %v", got, exp)
	}
}

func TestGroupRevokeDisconnectsAllMembers(t *testing.T) {
	_, a, addr, p := setupGroup(t)
	inv := a.createGroupInvite(t, "g", "", false)
	postForm(t, addr, inv.Code, "张三")
	c1, c2 := holdConn(t, p[0]), holdConn(t, p[1])

	a.send(t, protocol.RevokeLeaseMessage{Type: protocol.TypeRevokeLease, GroupID: "g"})
	expectClosed(t, c1)
	expectClosed(t, c2)
	if hit(p[0]) == "OK" || hit(p[1]) == "OK" {
		t.Fatal("members still open after revoking the group grant")
	}
}

func TestGroupApprovalFlow(t *testing.T) {
	m, a, addr, p := setupGroup(t)
	inv := a.createGroupInvite(t, "g", "", true)

	status, loc, _ := postForm(t, addr, inv.Code, "李四")
	if status != 303 || !strings.HasPrefix(loc, "/auth/status?id=") {
		t.Fatalf("approval invite should redirect to the status page: %d %q", status, loc)
	}
	pending := m.state.pendingList(groupScope("g"))
	if len(pending) != 1 || pending[0].GroupID != "g" || pending[0].TunnelID != "" {
		t.Fatalf("pending request should belong to the group: %+v", pending)
	}
	if _, body := httpGet(t, addr, loc, nil); !strings.Contains(body, "等待管理员批准") || !strings.Contains(body, "10.0.1.12:22") {
		t.Fatalf("pending page: %s", body)
	}
	if hit(p[0]) == "OK" {
		t.Fatal("must stay closed until approved")
	}

	a.send(t, protocol.DecideRequestMessage{Type: protocol.TypeDecideRequest, GroupID: "g", RequestID: pending[0].ID, Approve: true})
	waitFor(t, func() bool { return hit(p[0]) == "OK" && hit(p[1]) == "OK" })
	if _, body := httpGet(t, addr, loc, nil); !strings.Contains(body, "申请已批准") {
		t.Fatalf("status page after approval: %s", body)
	}
}

func TestGroupStateDroppedWhenLastMemberCloses(t *testing.T) {
	_, a, addr, _ := setupGroup(t)
	inv := a.createGroupInvite(t, "g", "", false)

	a.send(t, protocol.UnregisterMessage{Type: protocol.TypeUnregister, TunnelID: "m1"})
	a.next(t)
	if status, body := httpGet(t, addr, "/i/"+inv.Code, nil); status != 200 || !strings.Contains(body, "（1 个地址）") {
		t.Fatalf("invite should still work with one open member: %d", status)
	}

	a.send(t, protocol.UnregisterMessage{Type: protocol.TypeUnregister, TunnelID: "m2"})
	a.next(t)
	if status, _ := httpGet(t, addr, "/i/"+inv.Code, nil); status != 404 {
		t.Fatalf("invite must be gone once every member is closed, got %d", status)
	}
}

func TestGroupMembershipChangesFollowAccess(t *testing.T) {
	_, a, addr, p := setupGroup(t)
	inv := a.createGroupInvite(t, "g", "", false)
	postForm(t, addr, inv.Code, "张三")
	c2 := holdConn(t, p[1])

	// 移出组：组授权不再适用，现有连接断开
	out := groupMember("m2", p[1], "10.0.1.12:22")
	out.GroupID, out.GroupName = "", ""
	if resp := a.register(t, protocol.TypeUpdateTunnel, out); !resp.Success {
		t.Fatal(resp.Error)
	}
	expectClosed(t, c2)
	if hit(p[1]) == "OK" || hit(p[0]) != "OK" {
		t.Fatal("only remaining members keep the group grant")
	}

	// 后加入组的通道自动获得组授权
	in := groupMember("other", p[2], "10.0.1.13:22")
	if resp := a.register(t, protocol.TypeUpdateTunnel, in); !resp.Success {
		t.Fatal(resp.Error)
	}
	if hit(p[2]) != "OK" {
		t.Fatal("a tunnel joining the group should get the group grant")
	}
}

func TestTunnelAndGroupLeasesAreIndependent(t *testing.T) {
	m, _, _, p := setupGroup(t)
	exp := time.Now().Add(time.Hour)
	grantTestLease(m, "m1", "127.0.0.1", exp)
	grantTestLease(m, groupScope("g"), "127.0.0.1", exp)

	m.revokeLease(groupScope("g"), "127.0.0.1")
	if hit(p[0]) != "OK" {
		t.Fatal("the per-tunnel grant must survive revoking the group grant")
	}
	if hit(p[1]) == "OK" {
		t.Fatal("member without its own grant must close")
	}
}

func TestDeleteGroupDropsSharedAccess(t *testing.T) {
	m, a, addr, p := setupGroup(t)
	inv := a.createGroupInvite(t, "g", "", false)
	postForm(t, addr, inv.Code, "张三")

	a.send(t, protocol.DeleteGroupMessage{Type: protocol.TypeDeleteGroup, GroupID: "g"})
	waitFor(t, func() bool { return hit(p[0]) != "OK" })
	if _, _, ok := m.state.findInvite(inv.Code); ok {
		t.Fatal("invites of a dissolved group must be dropped")
	}
}

func TestGroupInviteNeedsAnOpenMember(t *testing.T) {
	m := newTestManager()
	addr := startServer(t, m)
	a := dialAgent(t, addr, testToken)
	a.expectAuth(t)
	a.send(t, protocol.CreateInviteMessage{Type: protocol.TypeCreateInvite, RequestID: "x", GroupID: "empty", ValidMinutes: 60, MaxUses: 1})
	var resp protocol.InviteRespMessage
	_ = json.Unmarshal(a.next(t), &resp)
	if resp.Success || !strings.Contains(resp.Error, "通道组") {
		t.Fatalf("invite for a group without open tunnels should fail: %+v", resp)
	}
}

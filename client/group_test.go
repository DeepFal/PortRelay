package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DeepFal/PortRelay/core/protocol"
)

func TestExpandTargets(t *testing.T) {
	cases := []struct {
		hosts, ports string
		want         []string
	}{
		{"10.0.1.11-13", "22", []string{"10.0.1.11:22", "10.0.1.12:22", "10.0.1.13:22"}},
		{"10.0.1.11, 10.0.1.12", "22, 8080", []string{"10.0.1.11:22", "10.0.1.11:8080", "10.0.1.12:22", "10.0.1.12:8080"}},
		{"10.0.1.11-10.0.1.12", "8080-8081", []string{"10.0.1.11:8080", "10.0.1.11:8081", "10.0.1.12:8080", "10.0.1.12:8081"}},
		{"db.internal:3306\n10.0.1.5", "22", []string{"db.internal:3306", "10.0.1.5:22"}},
		{"10.0.1.1，10.0.1.2、10.0.1.1", "22", []string{"10.0.1.1:22", "10.0.1.2:22"}},
		{"fd00::1", "22", []string{"[fd00::1]:22"}},
		{"10.0.2.9:5432", "", []string{"10.0.2.9:5432"}},
		{"10.0.1.11-13:22", "", []string{"10.0.1.11:22", "10.0.1.12:22", "10.0.1.13:22"}},
		{"10.0.1.11:8080-8081, db_1.internal:3306", "", []string{"10.0.1.11:8080", "10.0.1.11:8081", "db_1.internal:3306"}},
	}
	for _, c := range cases {
		got, err := ExpandTargets(c.hosts, c.ports)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ExpandTargets(%q, %q) = %v, %v; want %v", c.hosts, c.ports, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{
		{"", "22"},
		{"10.0.1.11", ""},
		{"bad host!", "22"},
		{"10.0.1.1-200", "22"},
		{"10.0.1.13-11", "22"},
		{"10.0.1.11", "0"},
		{"10.0.1.11", "70000"},
		{"10.0.1.1-40", "22, 23"},
		{"10.0.1.11-13", ""},
		{"10.0.1.11-300:22", ""},
		{"10.0.1.11:0", ""},
	} {
		if got, err := ExpandTargets(bad[0], bad[1]); err == nil {
			t.Errorf("ExpandTargets(%q, %q) should fail, got %v", bad[0], bad[1], got)
		}
	}
}

func TestMemberNames(t *testing.T) {
	if got := memberNames("订单服务", []string{"10.0.1.11:22", "10.0.1.12:22"}); got[0] != "订单服务 (10.0.1.11)" {
		t.Errorf("single port should name by host: %v", got)
	}
	if got := memberNames("订单服务", []string{"10.0.1.11:22", "10.0.1.11:8080"}); got[1] != "订单服务 (10.0.1.11:8080)" {
		t.Errorf("several ports should name by host:port: %v", got)
	}
}

func TestAllocatePortsPrefersContiguousBlock(t *testing.T) {
	cm := newTestConfig(t, "127.0.0.1:1", testToken)
	for _, p := range []int{33660, 33662} {
		if _, err := cm.AddTunnel(protocol.TunnelConfig{Name: fmt.Sprint(p), Target: "10.0.0.1:22", ListenPort: p}); err != nil {
			t.Fatal(err)
		}
	}
	cm.mu.Lock()
	three, err3 := cm.allocatePortsLocked(3)
	one, err1 := cm.allocatePortsLocked(1)
	cm.mu.Unlock()
	if err3 != nil || !reflect.DeepEqual(three, []int{33663, 33664, 33665}) {
		t.Fatalf("3 ports: %v %v", three, err3)
	}
	if err1 != nil || !reflect.DeepEqual(one, []int{33661}) {
		t.Fatalf("1 port: %v %v", one, err1)
	}
}

type batchResp struct {
	Group   *TunnelGroup            `json:"group"`
	Tunnels []protocol.TunnelConfig `json:"tunnels"`
}

// addTunnels submits the add-tunnel form
func (e *env) addTunnels(t *testing.T, body map[string]any) batchResp {
	t.Helper()
	status, raw := e.call(t, "POST", "/api/tunnels/batch", body)
	want := http.StatusCreated
	if body["dry_run"] == true {
		want = http.StatusOK
	}
	if status != want {
		t.Fatalf("add tunnels %v: %d %s", body, status, raw)
	}
	var out batchResp
	_ = json.Unmarshal(raw, &out)
	return out
}

func (e *env) group(t *testing.T, id string) GroupView {
	t.Helper()
	_, body := e.call(t, "GET", "/api/groups", nil)
	var list []GroupView
	_ = json.Unmarshal(body, &list)
	for _, g := range list {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("group %s not found", id)
	return GroupView{}
}

func grantViaInvite(t *testing.T, e *env, code, name string) {
	t.Helper()
	resp, err := http.PostForm(fmt.Sprintf("http://%s/auth", e.addr), url.Values{"code": {code}, "name": {name}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("grant: %d", resp.StatusCode)
	}
}

func TestGroupEndToEnd(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)
	echo1, echo2 := startEcho(t), startEcho(t)

	g := e.addTunnels(t, map[string]any{
		"name": "订单服务", "owner": "运维组", "target": echo1 + ", " + echo2, "new_group": "订单服务",
		"whitelist": []string{}, "idle_close_minutes": 60, "duration_minutes": 480, "max_bandwidth_mb": 5,
	})
	if g.Group == nil || g.Group.Name != "订单服务" || g.Group.Owner != "运维组" {
		t.Fatalf("new group: %+v", g.Group)
	}
	if len(g.Tunnels) != 2 || g.Tunnels[1].ListenPort != g.Tunnels[0].ListenPort+1 {
		t.Fatalf("members should get a contiguous port block: %+v", g.Tunnels)
	}
	m1, m2 := g.Tunnels[0], g.Tunnels[1]
	for _, m := range g.Tunnels {
		if m.GroupID != g.Group.ID || m.Owner != "运维组" || len(m.Whitelist) != 0 || !strings.HasPrefix(m.Name, "订单服务 (") {
			t.Fatalf("member not set up as a link-only group member: %+v", m)
		}
		// 表单里的每一项设置都用于每个成员
		if *m.IdleCloseMinutes != 60 || m.DurationMinutes != 480 || m.MaxBandwidthMB != 5 || m.ExpiresAt == nil {
			t.Fatalf("member lost form settings: %+v", m)
		}
	}
	waitFor(t, "members online", func() bool {
		return e.tunnel(t, m1.ID).Metrics.Status == "online" && e.tunnel(t, m2.ID).Metrics.Status == "online"
	})
	if _, err := echoVia(m1.ListenPort, "x"); err == nil {
		t.Fatal("group members are link-only by default")
	}

	status, body := e.call(t, "POST", "/api/groups/"+g.Group.ID+"/invites", map[string]any{"note": "小王", "lease_duration": "8h"})
	if status != http.StatusCreated {
		t.Fatalf("group invite: %d %s", status, body)
	}
	var inv struct {
		Invite    protocol.Invite `json:"invite"`
		ShareText string          `json:"share_text"`
	}
	_ = json.Unmarshal(body, &inv)
	for _, want := range []string{"通道组：订单服务（2 个地址）", fmt.Sprintf("127.0.0.1:%d → %s", m1.ListenPort, echo1), fmt.Sprintf("127.0.0.1:%d → %s", m2.ListenPort, echo2), "8 小时"} {
		if !strings.Contains(inv.ShareText, want) {
			t.Errorf("share text missing %q:\n%s", want, inv.ShareText)
		}
	}
	waitFor(t, "group invite listed", func() bool { return len(e.group(t, g.Group.ID).Metrics.Invites) == 1 })
	if status, _ := e.call(t, "GET", "/api/groups/"+g.Group.ID+"/invites/"+inv.Invite.Code+"/share", nil); status != http.StatusOK {
		t.Fatalf("share of existing group invite: %d", status)
	}

	grantViaInvite(t, e, inv.Invite.Code, "小王")
	for _, m := range g.Tunnels {
		if got, err := echoVia(m.ListenPort, "hi"); err != nil || got != "hi" {
			t.Fatalf("member %d not reachable after group grant: %q %v", m.ListenPort, got, err)
		}
	}
	waitFor(t, "group lease started", func() bool {
		l := e.group(t, g.Group.ID).Metrics.DynamicLeases
		return len(l) == 1 && l[0].Name == "小王" && !l[0].AwaitingConnect
	})
	if gv := e.group(t, g.Group.ID); !reflect.DeepEqual(gv.TunnelIDs, []string{m1.ID, m2.ID}) || gv.RateHistory == nil {
		t.Fatalf("group view: %+v", gv)
	}

	if status, _ := e.call(t, "POST", "/api/groups/"+g.Group.ID+"/leases/revoke", map[string]string{}); status != http.StatusOK {
		t.Fatal("revoke group leases failed")
	}
	waitFor(t, "group access revoked", func() bool {
		_, err1 := echoVia(m1.ListenPort, "x")
		_, err2 := echoVia(m2.ListenPort, "x")
		return err1 != nil && err2 != nil
	})

	// 整组关闭 / 开启
	e.call(t, "POST", "/api/groups/"+g.Group.ID+"/toggle", map[string]bool{"enabled": false})
	if e.tunnel(t, m1.ID).Enabled || e.tunnel(t, m2.ID).Enabled {
		t.Fatal("group toggle should close every member")
	}
	e.call(t, "POST", "/api/groups/"+g.Group.ID+"/toggle", map[string]bool{"enabled": true})
	if !e.tunnel(t, m1.ID).Enabled || !e.tunnel(t, m2.ID).Enabled {
		t.Fatal("group toggle should open every member")
	}

	// 解散：成员保留为独立通道
	if status, _ := e.call(t, "DELETE", "/api/groups/"+g.Group.ID, nil); status != http.StatusOK {
		t.Fatal("dissolve failed")
	}
	if e.tunnel(t, m1.ID).GroupID != "" || len(e.cm.Get().Groups) != 0 {
		t.Fatal("dissolved group should leave standalone tunnels")
	}
}

func TestGroupFromExistingTunnelsRenameAndMoveOut(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)
	var ids []string
	var ports []int
	for i := 0; i < 2; i++ {
		port := freeRelayPort(t, ports...)
		_, body := e.call(t, "POST", "/api/tunnels", map[string]any{"name": fmt.Sprint("t", i), "target": "127.0.0.1:1", "listen_port": port})
		var tc protocol.TunnelConfig
		_ = json.Unmarshal(body, &tc)
		ids, ports = append(ids, tc.ID), append(ports, port)
	}

	status, body := e.call(t, "POST", "/api/groups", map[string]any{"name": "支付服务", "tunnel_ids": ids})
	if status != http.StatusCreated {
		t.Fatalf("group from existing tunnels: %d %s", status, body)
	}
	var g batchResp
	_ = json.Unmarshal(body, &g)
	if e.tunnel(t, ids[0]).GroupID != g.Group.ID || e.tunnel(t, ids[1]).GroupID != g.Group.ID {
		t.Fatal("existing tunnels should join the group")
	}
	_, body = e.call(t, "POST", "/api/groups/"+g.Group.ID+"/invites", map[string]any{})
	var inv struct {
		Invite protocol.Invite `json:"invite"`
	}
	_ = json.Unmarshal(body, &inv)
	page := func() string {
		resp, err := http.Get(fmt.Sprintf("http://%s/i/%s", e.addr, inv.Invite.Code))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if p := page(); !strings.Contains(p, "支付服务（2 个地址）") {
		t.Fatalf("invite page should list the group: %s", p)
	}

	// 改名会同步到服务端
	if status, body := e.call(t, "PUT", "/api/groups/"+g.Group.ID, map[string]any{"name": "支付服务后端"}); status != http.StatusOK {
		t.Fatalf("rename: %d %s", status, body)
	}
	waitFor(t, "new group name on the invite page", func() bool { return strings.Contains(page(), "支付服务后端（2 个地址）") })

	// 移出组；不能加入不存在的组
	if status, _ := e.call(t, "PUT", "/api/tunnels/"+ids[0], map[string]any{"group_id": ""}); status != http.StatusOK {
		t.Fatal("move out failed")
	}
	waitFor(t, "invite page lists the remaining member", func() bool { return strings.Contains(page(), "（1 个地址）") })
	if status, _ := e.call(t, "PUT", "/api/tunnels/"+ids[0], map[string]any{"group_id": "nope"}); status != http.StatusBadRequest {
		t.Fatal("joining an unknown group must fail")
	}

	// 一个通道只能属于一个组；组名不能重复
	if status, _ := e.call(t, "POST", "/api/groups", map[string]any{"name": "其它", "tunnel_ids": []string{ids[1]}}); status != http.StatusBadRequest {
		t.Fatal("a tunnel cannot be in two groups")
	}
	if status, _ := e.call(t, "POST", "/api/groups", map[string]any{"name": "支付服务后端", "tunnel_ids": []string{ids[0]}}); status != http.StatusBadRequest {
		t.Fatal("duplicate group names must be rejected")
	}

	// 解散并删除成员
	if status, _ := e.call(t, "DELETE", "/api/groups/"+g.Group.ID+"?delete_members=1", nil); status != http.StatusOK {
		t.Fatal("delete group with members failed")
	}
	if _, ok := e.cm.FindTunnel(ids[1]); ok {
		t.Fatal("members should be deleted with the group")
	}
	if _, ok := e.cm.FindTunnel(ids[0]); !ok {
		t.Fatal("the tunnel moved out earlier must remain")
	}
}

func TestAddTunnelFormPreviewAndRules(t *testing.T) {
	e := startEnv(t, testToken)

	// 预览：不保存，展开目标并预分配连续端口
	pv := e.addTunnels(t, map[string]any{"target": "10.0.1.11-12:22", "dry_run": true})
	if len(pv.Tunnels) != 2 || pv.Tunnels[0].Target != "10.0.1.11:22" || pv.Tunnels[1].ListenPort != pv.Tunnels[0].ListenPort+1 {
		t.Fatalf("preview: %+v", pv.Tunnels)
	}
	if len(e.cm.Get().Tunnels) != 0 {
		t.Fatal("preview must not create tunnels")
	}

	// 多个目标必须放进通道组
	if status, _ := e.call(t, "POST", "/api/tunnels/batch", map[string]any{"name": "x", "target": "10.0.1.11-12:22"}); status != http.StatusBadRequest {
		t.Fatal("several targets without a group must be rejected")
	}

	// 单个目标：名称和端口按填写的来，不分组
	port := freeRelayPort(t)
	one := e.addTunnels(t, map[string]any{"name": "MySQL", "target": "10.0.2.9:3306", "listen_port": port})
	if one.Group != nil || len(one.Tunnels) != 1 || one.Tunnels[0].Name != "MySQL" || one.Tunnels[0].ListenPort != port {
		t.Fatalf("single tunnel: %+v", one)
	}

	// 加入已有组且不填名称：按组名命名
	grp := e.addTunnels(t, map[string]any{"name": "订单服务", "target": "10.0.1.11:22", "new_group": "订单服务"})
	if grp.Tunnels[0].Name != "订单服务" {
		t.Fatalf("a named single tunnel keeps its name: %+v", grp.Tunnels[0])
	}
	more := e.addTunnels(t, map[string]any{"target": "10.0.1.12-13:22", "group_id": grp.Group.ID})
	if more.Group.ID != grp.Group.ID || more.Tunnels[0].Name != "订单服务 (10.0.1.12)" || more.Tunnels[0].GroupID != grp.Group.ID {
		t.Fatalf("members added to an existing group: %+v", more)
	}
	if status, _ := e.call(t, "POST", "/api/tunnels/batch", map[string]any{"name": "y", "target": "10.0.3.1:22", "new_group": "订单服务"}); status != http.StatusBadRequest {
		t.Fatal("duplicate group names must be rejected")
	}

	// 编辑通道时新建通道组
	status, body := e.call(t, "PUT", "/api/tunnels/"+one.Tunnels[0].ID, map[string]any{"new_group": "数据库"})
	var moved protocol.TunnelConfig
	_ = json.Unmarshal(body, &moved)
	groups := e.cm.Get().Groups
	if status != http.StatusOK || len(groups) != 2 || groups[1].Name != "数据库" || moved.GroupID != groups[1].ID {
		t.Fatalf("edit into a new group: %d %s %+v", status, body, groups)
	}
}

func TestGroupIdleCloseIsLinked(t *testing.T) {
	e := startEnvWith(t, testToken, func(a *Agent) { a.idleUnit = 3 * time.Second })
	waitFor(t, "agent connected", e.agent.IsConnected)
	g := e.addTunnels(t, map[string]any{"name": "g", "target": "127.0.0.1:1, 127.0.0.1:2", "new_group": "g", "idle_close_minutes": 1})
	a, b := g.Tunnels[0], g.Tunnels[1]
	waitFor(t, "members online", func() bool {
		return e.tunnel(t, a.ID).Metrics.Status == "online" && e.tunnel(t, b.ID).Metrics.Status == "online"
	})

	// 只给成员 a 发邀请：b 也算在用，不会单独闲置关闭
	_, ib := e.call(t, "POST", "/api/tunnels/"+a.ID+"/invites", map[string]any{})
	var ir struct {
		Invite protocol.Invite `json:"invite"`
	}
	_ = json.Unmarshal(ib, &ir)
	time.Sleep(6 * time.Second)
	if !e.tunnel(t, b.ID).Enabled {
		t.Fatal("a member must stay open while another member is in use")
	}
	e.call(t, "DELETE", "/api/tunnels/"+a.ID+"/invites/"+ir.Invite.Code, nil)
	waitFor(t, "both members closed together", func() bool { return !e.tunnel(t, a.ID).Enabled && !e.tunnel(t, b.ID).Enabled })

	// 组邀请同样让整组保持开启
	e.call(t, "POST", "/api/groups/"+g.Group.ID+"/toggle", map[string]bool{"enabled": true})
	waitFor(t, "members online again", func() bool {
		return e.tunnel(t, a.ID).Metrics.Status == "online" && e.tunnel(t, b.ID).Metrics.Status == "online"
	})
	_, gb := e.call(t, "POST", "/api/groups/"+g.Group.ID+"/invites", map[string]any{})
	var gr struct {
		Invite protocol.Invite `json:"invite"`
	}
	_ = json.Unmarshal(gb, &gr)
	time.Sleep(6 * time.Second)
	if !e.tunnel(t, a.ID).Enabled || !e.tunnel(t, b.ID).Enabled {
		t.Fatal("an unused group invite keeps the whole group open")
	}
	e.call(t, "DELETE", "/api/groups/"+g.Group.ID+"/invites/"+gr.Invite.Code, nil)
	waitFor(t, "group closed after its invite was revoked", func() bool { return !e.tunnel(t, a.ID).Enabled && !e.tunnel(t, b.ID).Enabled })
}

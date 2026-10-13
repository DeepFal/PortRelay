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

type groupResp struct {
	Group   TunnelGroup             `json:"group"`
	Created []protocol.TunnelConfig `json:"created"`
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

	status, body := e.call(t, "POST", "/api/groups", map[string]any{"name": "订单服务", "owner": "运维组", "hosts": echo1 + ", " + echo2})
	if status != http.StatusCreated {
		t.Fatalf("create group: %d %s", status, body)
	}
	var g groupResp
	_ = json.Unmarshal(body, &g)
	if len(g.Created) != 2 || g.Created[1].ListenPort != g.Created[0].ListenPort+1 {
		t.Fatalf("members should get a contiguous port block: %+v", g.Created)
	}
	m1, m2 := g.Created[0], g.Created[1]
	for _, m := range g.Created {
		if m.GroupID != g.Group.ID || m.Owner != "运维组" || len(m.Whitelist) != 0 || !strings.HasPrefix(m.Name, "订单服务 (") {
			t.Fatalf("member not set up as a link-only group member: %+v", m)
		}
	}
	waitFor(t, "members online", func() bool {
		return e.tunnel(t, m1.ID).Metrics.Status == "online" && e.tunnel(t, m2.ID).Metrics.Status == "online"
	})
	if _, err := echoVia(m1.ListenPort, "x"); err == nil {
		t.Fatal("group members are link-only by default")
	}

	status, body = e.call(t, "POST", "/api/groups/"+g.Group.ID+"/invites", map[string]any{"note": "小王", "lease_duration": "8h"})
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
	for _, m := range g.Created {
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
	var g groupResp
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

func TestGroupPreviewDoesNotSave(t *testing.T) {
	e := startEnv(t, testToken)
	status, body := e.call(t, "POST", "/api/groups/preview", map[string]any{"name": "订单服务", "hosts": "10.0.1.11-12", "ports": "22"})
	if status != http.StatusOK {
		t.Fatalf("preview: %d %s", status, body)
	}
	var out struct {
		Members []struct {
			Name       string `json:"name"`
			Target     string `json:"target"`
			ListenPort int    `json:"listen_port"`
		} `json:"members"`
	}
	_ = json.Unmarshal(body, &out)
	if len(out.Members) != 2 || out.Members[0].Name != "订单服务 (10.0.1.11)" || out.Members[1].ListenPort != out.Members[0].ListenPort+1 {
		t.Fatalf("preview: %+v", out.Members)
	}
	if len(e.cm.Get().Tunnels) != 0 {
		t.Fatal("preview must not create tunnels")
	}
	if status, _ := e.call(t, "POST", "/api/groups/preview", map[string]any{"hosts": "10.0.1.11"}); status != http.StatusBadRequest {
		t.Fatal("missing ports should be rejected")
	}
}

func TestGroupIdleCloseIsLinked(t *testing.T) {
	e := startEnvWith(t, testToken, func(a *Agent) { a.idleUnit = 3 * time.Second })
	waitFor(t, "agent connected", e.agent.IsConnected)
	_, body := e.call(t, "POST", "/api/groups", map[string]any{"name": "g", "hosts": "127.0.0.1", "ports": "1, 2", "idle_close_minutes": 1})
	var g groupResp
	_ = json.Unmarshal(body, &g)
	a, b := g.Created[0], g.Created[1]
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

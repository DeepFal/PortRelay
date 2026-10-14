package client

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestExtendReopensClosedTunnel(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)
	echo := startEcho(t)
	// 最长开启 10 分钟的仅授权链接通道
	one := e.addTunnels(t, map[string]any{"name": "db", "target": echo, "duration_minutes": 10, "whitelist": []string{}})
	tn := one.Tunnels[0]
	waitFor(t, "online", func() bool { return e.tunnel(t, tn.ID).Metrics.Status == "online" })

	_, body := e.call(t, "POST", "/api/tunnels/"+tn.ID+"/invites", map[string]any{"lease_duration": "30m"})
	var inv struct {
		Invite struct{ Code string } `json:"invite"`
	}
	_ = json.Unmarshal(body, &inv)
	grantViaInvite(t, e, inv.Invite.Code, "张三")
	if got, err := echoVia(tn.ListenPort, "hi"); err != nil || got != "hi" {
		t.Fatalf("granted device cannot connect: %q %v", got, err)
	}

	// 关闭通道：设备仍可在 2 小时内被延长
	e.call(t, "POST", "/api/tunnels/"+tn.ID+"/toggle", map[string]bool{"enabled": false})
	waitFor(t, "renewable after close", func() bool {
		rs := e.tunnel(t, tn.ID).Renewals
		return len(rs) == 1 && rs[0].IP == "127.0.0.1" && rs[0].EndedAt != nil
	})

	status, body := e.call(t, "POST", "/api/tunnels/"+tn.ID+"/leases/extend", map[string]any{"ip": "127.0.0.1", "minutes": 60})
	if status != http.StatusOK {
		t.Fatalf("extend: %d %s", status, body)
	}
	var out struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	_ = json.Unmarshal(body, &out)
	if d := time.Until(out.ExpiresAt); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("an ended access is extended from now: %v", d)
	}
	reopened := e.tunnel(t, tn.ID)
	if !reopened.Enabled || reopened.ExpiresAt == nil || reopened.ExpiresAt.Before(out.ExpiresAt) {
		t.Fatalf("the tunnel must reopen and stay open until the extended access ends: %+v", reopened.TunnelConfig)
	}
	waitFor(t, "reachable again", func() bool {
		got, err := echoVia(tn.ListenPort, "x")
		return err == nil && got == "x"
	})

	// 没有可延长授权的设备：不打开通道
	e.call(t, "POST", "/api/tunnels/"+tn.ID+"/toggle", map[string]bool{"enabled": false})
	if status, _ := e.call(t, "POST", "/api/tunnels/"+tn.ID+"/leases/extend", map[string]any{"ip": "10.1.2.3", "minutes": 30}); status != http.StatusConflict {
		t.Fatalf("unknown device: %d", status)
	}
	if e.tunnel(t, tn.ID).Enabled {
		t.Fatal("a refused extension must not reopen the tunnel")
	}
}

func TestExtendReopensWholeGroup(t *testing.T) {
	e := startEnv(t, testToken)
	waitFor(t, "agent connected", e.agent.IsConnected)
	echo1, echo2 := startEcho(t), startEcho(t)
	g := e.addTunnels(t, map[string]any{"name": "订单服务", "target": echo1 + ", " + echo2, "new_group": "订单服务"})
	gid := g.Group.ID
	waitFor(t, "online", func() bool {
		return e.tunnel(t, g.Tunnels[0].ID).Metrics.Status == "online" && e.tunnel(t, g.Tunnels[1].ID).Metrics.Status == "online"
	})
	_, body := e.call(t, "POST", "/api/groups/"+gid+"/invites", map[string]any{})
	var inv struct {
		Invite struct{ Code string } `json:"invite"`
	}
	_ = json.Unmarshal(body, &inv)
	grantViaInvite(t, e, inv.Invite.Code, "小王")

	e.call(t, "POST", "/api/groups/"+gid+"/toggle", map[string]bool{"enabled": false})
	waitFor(t, "group renewable after close", func() bool { return len(e.group(t, gid).Renewals) == 1 })
	if status, body := e.call(t, "POST", "/api/groups/"+gid+"/leases/extend", map[string]any{"ip": "127.0.0.1", "minutes": 30}); status != http.StatusOK {
		t.Fatalf("extend group: %d %s", status, body)
	}
	for _, m := range g.Tunnels {
		if !e.tunnel(t, m.ID).Enabled {
			t.Fatal("extending a group's access reopens every member")
		}
	}
	waitFor(t, "every member reachable", func() bool {
		_, err1 := echoVia(g.Tunnels[0].ListenPort, "a")
		_, err2 := echoVia(g.Tunnels[1].ListenPort, "b")
		return err1 == nil && err2 == nil
	})
}

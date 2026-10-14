// webpanel 用模拟数据提供 Agent 网页面板，用于 README 截图（tools/ui-snapshots/web.sh 调用）。
//
//	go run ./tools/ui-snapshots/webpanel cmd/portrelay-agent/web/index.html
//
// 只监听 127.0.0.1 的随机端口，启动后在标准输出打印面板地址。
// 数据与 tools/ui-snapshots/main.swift 的 makeDemoVM 保持一致，App 截图和网页截图展示的是同一组通道。
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"time"
)

var base = time.Now()

func at(sec float64) string {
	return base.Add(time.Duration(sec * float64(time.Second))).Format(time.RFC3339Nano)
}

// demoRate 与 main.swift 的同名函数相同：几段查询突发叠加平稳传输
func demoRate(k int, seed, level, burst float64) int64 {
	x := float64(k)
	v := level * (0.55 + 0.25*math.Sin(x/9+seed) + 0.2*math.Sin(x/3.7+seed*2))
	for c := -60; c <= 600; c += 21 {
		h := 0.35 + 0.65*math.Abs(math.Sin(float64(c)*0.731+seed))
		v += burst * h * math.Exp(-math.Pow((x-float64(c)-7*math.Sin(float64(c)+seed))/3.2, 2))
	}
	return int64(math.Max(0, v))
}

type series struct {
	End  int64   `json:"end"`
	Step int     `json:"step"`
	Up   []int64 `json:"up"`
	Down []int64 `json:"down"`
}

func demoSeries(seed, level, burst, upScale float64, points int) series {
	s := series{End: base.Unix(), Step: 5, Up: make([]int64, 120), Down: make([]int64, 120)}
	for i := 0; i < points; i++ {
		s.Down[i] = demoRate(i, seed, level, burst)
		s.Up[i] = demoRate(i, seed+1.7, level*upScale, burst*upScale)
	}
	return s
}

func sum(a, b series) series {
	s := series{End: a.End, Step: a.Step}
	for i := range a.Up {
		s.Up = append(s.Up, a.Up[i]+b.Up[i])
		s.Down = append(s.Down, a.Down[i]+b.Down[i])
	}
	return s
}

func obj(format string, args ...any) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprintf(format, args...)), &m); err != nil {
		log.Fatal(err)
	}
	return m
}

func member(id, host string, port, conns int, firstAgo float64, connectedSec int, bytesIn, bytesOut int64) map[string]any {
	first := ""
	if firstAgo != 0 {
		first = fmt.Sprintf(`"first_connected_at":%q,`, at(firstAgo))
	}
	return obj(`{"id":%q,"name":"订单服务 (%s)","target":"%s:22","listen_port":%d,"whitelist":[],"enabled":true,"group_id":"g1","group_name":"订单服务",
	  "owner":"运维组","started_at":%q,"effective_idle_close_minutes":30,"idle_close_minutes":30,%s"connected_sec":%d,
	  "activity":{"in_use":true,"connected_sec":%d},
	  "metrics":{"tunnel_id":%q,"status":"online","active_conns":%d,"total_conns":%d,"bytes_in":%d,"bytes_out":%d,"blocked_attempts":0}}`,
		id, host, host, port, at(-1800), first, connectedSec, connectedSec, id, conns, conns*5, bytesIn, bytesOut)
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: webpanel path/to/index.html")
	}
	page, err := os.ReadFile(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}

	tunnels := []map[string]any{
		obj(`{"id":"t1","name":"MySQL 数据库","target":"10.0.1.16:3306","listen_port":33660,"whitelist":[],"enabled":true,
		  "owner":"数据组","duration_minutes":240,"started_at":%q,"expires_at":%q,"historical_bytes_in":1288490188,"historical_bytes_out":53687091200,
		  "idle_close_minutes":30,"effective_idle_close_minutes":30,"first_connected_at":%q,"connected_sec":1520,
		  "activity":{"in_use":true,"connected_sec":1525},
		  "metrics":{"tunnel_id":"t1","status":"online","active_conns":3,"total_conns":1287,"bytes_in":245039104,"bytes_out":8703180,"blocked_attempts":12,
		    "dynamic_leases":[{"ip":"192.168.1.21","expires_at":%q,"name":"张三","invite":"a1","duration":"2h","started_at":%q},
		                      {"ip":"192.168.1.35","expires_at":%q,"name":"王小明","invite":"a2","duration":"8h","awaiting_connect":true}],
		    "pending":[{"id":"r1","tunnel_id":"t1","name":"李四","ip":"192.168.1.88","invite":"a3","at":%q,"status":"pending"}]}}`,
			at(-3725), at(10675), at(-3300), at(7000), at(-200), at(80000), at(-60)),
		member("g1a", "10.0.3.11", 33670, 1, -1500, 1320, 3203180, 18039104),
		member("g1b", "10.0.3.12", 33671, 2, -1400, 1100, 2203180, 12039104),
		member("g1c", "10.0.3.13", 33672, 0, 0, 0, 0, 0),
		obj(`{"id":"t2","name":"Grafana 监控面板","target":"10.0.2.30:3000","listen_port":33661,"whitelist":["192.168.1.0/24"],"enabled":true,
		  "owner":"运维组","duration_minutes":0,"started_at":%q,"effective_idle_close_minutes":0,"idle_close_minutes":0,"first_connected_at":%q,"connected_sec":36120,
		  "activity":{"in_use":true,"connected_sec":36120},
		  "metrics":{"tunnel_id":"t2","status":"online","active_conns":2,"total_conns":342,"bytes_in":52039104,"bytes_out":2703180,"blocked_attempts":0}}`,
			at(-50000), at(-49000)),
		obj(`{"id":"t3","name":"SSH 跳板机（测试环境）","target":"10.0.8.3:22","listen_port":33662,"whitelist":[],"enabled":true,
		  "owner":"运维组","duration_minutes":0,"started_at":%q,"effective_idle_close_minutes":30,"idle_close_minutes":30,"first_connected_at":%q,"connected_sec":640,
		  "activity":{"in_use":false,"idle_since":%q,"auto_close_at":%q,"connected_sec":640},
		  "metrics":{"tunnel_id":"t3","status":"online","active_conns":0,"total_conns":4,"bytes_in":1983000,"bytes_out":204000,"blocked_attempts":0}}`,
			at(-2100), at(-2000), at(-720), at(1080)),
		obj(`{"id":"t4","name":"Redis 缓存","target":"10.0.1.20:6379","listen_port":33663,"whitelist":[],"enabled":false,"last_duration_sec":1905,"duration_minutes":0,
		  "effective_idle_close_minutes":30,"last_connected_sec":742,"close_reason":"闲置 30 分钟自动关闭","historical_bytes_in":3145728,"historical_bytes_out":524288,
		  "metrics":{"tunnel_id":"t4","status":"disabled"}}`),
	}
	rates := map[string]series{
		"t1":  demoSeries(0.4, 26000, 150000, 0.08, 120),
		"t2":  demoSeries(2.1, 9000, 30000, 0.15, 120),
		"t3":  demoSeries(6, 1500, 9000, 0.2, 30), // 前一段时间有流量，之后闲置
		"g1a": demoSeries(3.3, 4000, 26000, 0.3, 120),
		"g1b": demoSeries(4.6, 4000, 26000, 0.3, 120),
		"g1c": demoSeries(0, 0, 0, 0, 0),
	}
	for _, t := range tunnels {
		if s, ok := rates[t["id"].(string)]; ok {
			t["rate_history"] = s
			m := t["metrics"].(map[string]any)
			m["rate_in"], m["rate_out"] = s.Up[len(s.Up)-1], s.Down[len(s.Down)-1]
		}
	}
	groups := []map[string]any{obj(`{"id":"g1","name":"订单服务","owner":"运维组","created_at":%q,"tunnel_ids":["g1a","g1b","g1c"],
	  "metrics":{"group_id":"g1",
	    "dynamic_leases":[{"ip":"192.168.1.42","expires_at":%q,"name":"小王","invite":"gx1","duration":"8h","started_at":%q}],
	    "pending":[{"id":"gr1","tunnel_id":"","group_id":"g1","name":"小李","ip":"192.168.1.57","invite":"gx2","at":%q,"status":"pending"}]}}`,
		at(-1800), at(27000), at(-1500), at(-40))}
	groups[0]["rate_history"] = sum(rates["g1a"], rates["g1b"])

	status := map[string]any{
		"connected": true, "server_addr": "192.168.1.10:33000", "public_host": "192.168.1.10",
		"invite_base": "http://192.168.1.10:33000/i/", "total_rate_history": sum(rates["t1"], rates["t2"]),
		"port_range": "33001 - 34000", "timestamp": base,
	}
	stream := map[string]any{"metrics": map[string]any{"active_conns": []any{}, "security_events": []any{}}, "tunnels": tunnels, "groups": groups}
	for k, v := range status {
		stream[k] = v
	}

	serveJSON := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("/api/status", serveJSON(status))
	mux.HandleFunc("/api/tunnels", serveJSON(tunnels))
	mux.HandleFunc("/api/groups", serveJSON(groups))
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(stream)
		fmt.Fprintf(w, "data: %s\n\n", b)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("http://%s/\n", ln.Addr())
	log.Fatal(http.Serve(ln, mux))
}

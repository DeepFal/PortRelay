import SwiftUI
import AppKit

let outDir = CommandLine.arguments.count > 1 ? CommandLine.arguments[1] : "."
let only = CommandLine.arguments.count > 2 ? CommandLine.arguments[2] : ""
_ = NSApplication.shared
NSApp.setActivationPolicy(.prohibited)

let iso: ISO8601DateFormatter = { let f = ISO8601DateFormatter(); f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]; return f }()
func t(_ secondsFromNow: Double) -> String { iso.string(from: Date().addingTimeInterval(secondsFromNow)) }

func decode<T: Decodable>(_ type: T.Type, _ json: String) -> T {
    do { return try JSONDecoder().decode(T.self, from: Data(json.utf8)) }
    catch { fatalError("mock decode \(T.self): \(error)") }
}

@MainActor
func makeVM() -> PortRelayViewModel {
    let vm = PortRelayViewModel(preview: true)
    vm.isConnected = true
    vm.serverAddr = "192.168.1.10:33000"
    vm.publicHost = "192.168.1.10"
    vm.inviteBase = "http://192.168.1.10:33000/i/"
    vm.tunnels = decode([TunnelItem].self, """
    [
     {"id":"t1","name":"MySQL 数据库 (10.0.1.16)","target":"10.0.1.16:3306","listen_port":33660,"whitelist":[],"enabled":true,
      "owner":"数据组","duration_minutes":120,"started_at":"\(t(-3725))","expires_at":"\(t(3475))","historical_bytes_in":1288490188,"historical_bytes_out":53687091200,
      "idle_close_minutes":30,"effective_idle_close_minutes":30,"first_connected_at":"\(t(-3300))","connected_sec":1520,
      "activity":{"in_use":true,"connected_sec":1525},
      "max_bandwidth_mb":10,
      "metrics":{"tunnel_id":"t1","status":"online","active_conns":4,"total_conns":1287,"bytes_in":245039104,"bytes_out":8703180,"rate_in":1740,"rate_out":46285,"blocked_attempts":12,
        "dynamic_leases":[{"ip":"192.168.1.21","expires_at":"\(t(7000))","name":"张三","invite":"a1","duration":"2h","started_at":"\(t(-200))"},{"ip":"192.168.1.35","expires_at":"\(t(80000))","name":"一个名字特别长的外包同事王小明","invite":"a2","duration":"2h","awaiting_connect":true}],
        "pending":[{"id":"r1","tunnel_id":"t1","name":"李四","ip":"192.168.1.88","invite":"a3","at":"\(t(-60))","status":"pending"}],
        "invites":[{"code":"a1","note":"张三","created_at":"\(t(-3600))","expires_at":"\(t(80000))","max_uses":1,"lease_duration":"today","uses":[{"name":"张三","ip":"192.168.1.21","at":"\(t(-3500))"}]},
                   {"code":"a3","created_at":"\(t(-60))","expires_at":"\(t(600000))","max_uses":0,"lease_duration":"1h","require_approval":true}]}},
     {"id":"t2","name":"SSH（10.0.8.3）测试环境 docker 后端跳板机入口","target":"10.0.8.3:22","listen_port":33661,"whitelist":["192.168.1.0/24","10.0.0.1"],"enabled":true,
      "owner":"运维组","duration_minutes":0,"started_at":"\(t(-50000))","effective_idle_close_minutes":30,"connected_sec":0,
      "activity":{"in_use":false,"idle_since":"\(t(-720))","auto_close_at":"\(t(1080))","connected_sec":0},
      "metrics":{"tunnel_id":"t2","status":"error","error":"port 33661 already in use by tunnel 3f2c9a7e-1b2d-4c5e-9f00-1234567890ab"}},
     {"id":"t3","name":"Redis 缓存","target":"10.0.1.20:6379","listen_port":33662,"whitelist":["*"],"enabled":false,"last_duration_sec":1905,"duration_minutes":0,"effective_idle_close_minutes":30,"last_connected_sec":742,"close_reason":"闲置 30 分钟自动关闭",
      "historical_bytes_in":1024,"historical_bytes_out":0}
    ]
    """)
    vm.activeConns = decode([ActiveClientInfo].self, """
    [
     {"conn_id":"c1","tunnel_id":"t1","client_ip":"192.168.1.21:52889","connected_at":"\(t(-600))","bytes_in":416358,"bytes_out":4089446,"rate_in":632,"rate_out":16486,"rate_in":632},
     {"conn_id":"c2","tunnel_id":"t1","client_ip":"192.168.1.21:50395","connected_at":"\(t(-577))","bytes_in":406528,"bytes_out":3984588,"rate_in":1126,"rate_out":29798},
     {"conn_id":"c3","tunnel_id":"t1","client_ip":"192.168.1.21:53023","connected_at":"\(t(-526))","bytes_in":244213760,"bytes_out":633856,"rate_in":0,"rate_out":0},
     {"conn_id":"c4","tunnel_id":"t1","client_ip":"[fe80::1c2d:3e4f:aa:bb]:61001","connected_at":"\(t(-30))","bytes_in":120,"bytes_out":88,"rate_in":0,"rate_out":0}
    ]
    """)
    vm.previewHistory = decode([ClientHistoryRecord].self, """
    [
     {"id":"h1","tunnel_id":"t1","tunnel_name":"MySQL","port":33660,"client_ip":"192.168.1.21:51000","connected_at":"\(t(-5000))","disconnected_at":"\(t(-4000))","duration_sec":1000,"bytes_in":1288490188,"bytes_out":5368709},
     {"id":"h2","tunnel_id":"t1","tunnel_name":"MySQL","port":33660,"client_ip":"192.168.1.21:51001","connected_at":"\(t(-3000))","disconnected_at":"\(t(-2990))","duration_sec":10,"bytes_in":2048,"bytes_out":4096},
     {"id":"h3","tunnel_id":"t1","tunnel_name":"MySQL","port":33660,"client_ip":"192.168.1.35:40000","connected_at":"\(t(-90000))","disconnected_at":"\(t(-86000))","duration_sec":4000,"bytes_in":99999999,"bytes_out":1}
    ]
    """)
    // 模拟近 10 分钟流量：几段查询突发 + 平稳传输
    func wave(_ seed: Double, base: Double, burst: Double) -> [Int64] {
        (0..<120).map { i in
            let x = Double(i)
            var v = base * (0.6 + 0.4 * sin(x / 7 + seed))
            for c in [18.0, 47.0, 83.0, 108.0] { v += burst * exp(-pow((x - c - seed) / 3.5, 2)) }
            return Int64(max(0, v))
        }
    }
    vm.tunnels[0].rate_history = RateSeriesItem(end: Int64(Date().timeIntervalSince1970), step: 5,
                                                up: wave(1, base: 1800, burst: 9000), down: wave(0, base: 22000, burst: 160000))
    vm.tunnels[1].rate_history = RateSeriesItem(end: Int64(Date().timeIntervalSince1970), step: 5,
                                                up: Array(repeating: 0, count: 120), down: Array(repeating: 0, count: 120))
    vm.totalRateHistory = vm.tunnels[0].rate_history
    vm.totalActiveConns = 4; vm.totalHistoricalConns = 1287; vm.totalRateIn = 1740; vm.totalRateOut = 46285
    vm.totalTransferred = 60000000000; vm.totalBlockedAttempts = 12
    vm.runDiagnostics()
    return vm
}

// MARK: - README 演示数据（render.sh readme / render.sh gif）

/// 平滑、确定性的模拟流量：k 为 5 秒桶的绝对序号，便于逐帧平移
func demoRate(_ k: Int, seed: Double, base: Double, burst: Double) -> Int64 {
    let x = Double(k)
    var v = base * (0.55 + 0.25 * sin(x / 9 + seed) + 0.2 * sin(x / 3.7 + seed * 2))
    for c in stride(from: -60, through: 600, by: 21) {
        let h = 0.35 + 0.65 * abs(sin(Double(c) * 0.731 + seed))
        v += burst * h * exp(-pow((x - Double(c) - 7 * sin(Double(c) + seed)) / 3.2, 2))
    }
    return Int64(max(0, v))
}

func demoSeries(tick: Int, seed: Double, base: Double, burst: Double, upScale: Double) -> RateSeriesItem {
    let down = (0..<120).map { demoRate($0 + tick, seed: seed, base: base, burst: burst) }
    let up = (0..<120).map { demoRate($0 + tick, seed: seed + 1.7, base: base * upScale, burst: burst * upScale) }
    return RateSeriesItem(end: Int64(Date().timeIntervalSince1970) + Int64(tick * 5), step: 5, up: up, down: down)
}

/// 演示数据的时间基准：逐帧渲染时各时间点保持不变，只有计数随 tick 增长
let demoBase = Date()

@MainActor
func makeDemoVM(tick: Int = 0) -> PortRelayViewModel {
    let vm = PortRelayViewModel(preview: true)
    let dt = Double(tick * 5)
    func t(_ s: Double) -> String { iso.string(from: demoBase.addingTimeInterval(s)) }
    vm.isConnected = true
    vm.serverAddr = "192.168.1.10:33000"
    vm.publicHost = "192.168.1.10"
    vm.inviteBase = "http://192.168.1.10:33000/i/"
    vm.tunnels = decode([TunnelItem].self, """
    [
     {"id":"t1","name":"MySQL 数据库","target":"10.0.1.16:3306","listen_port":33660,"whitelist":[],"enabled":true,
      "owner":"数据组","duration_minutes":240,"started_at":"\(t(-3725))","expires_at":"\(t(10675))","historical_bytes_in":1288490188,"historical_bytes_out":53687091200,
      "idle_close_minutes":30,"effective_idle_close_minutes":30,"first_connected_at":"\(t(-3300))","connected_sec":\(1520 + Int(dt)),
      "activity":{"in_use":true,"connected_sec":\(1525 + Int(dt))},
      "metrics":{"tunnel_id":"t1","status":"online","active_conns":3,"total_conns":1287,"bytes_in":\(245039104 + tick * 900000),"bytes_out":\(8703180 + tick * 40000),"rate_in":0,"rate_out":0,"blocked_attempts":12,
        "dynamic_leases":[{"ip":"192.168.1.21","expires_at":"\(t(7000))","name":"张三","invite":"a1","duration":"2h","started_at":"\(t(-200))"},{"ip":"192.168.1.35","expires_at":"\(t(80000))","name":"王小明","invite":"a2","duration":"8h","awaiting_connect":true}],
        "pending":[{"id":"r1","tunnel_id":"t1","name":"李四","ip":"192.168.1.88","invite":"a3","at":"\(t(-60))","status":"pending"}],
        "invites":[{"code":"a1","note":"张三","created_at":"\(t(-3600))","expires_at":"\(t(80000))","max_uses":1,"lease_duration":"2h","uses":[{"name":"张三","ip":"192.168.1.21","at":"\(t(-3500))"}]},
                   {"code":"a2","note":"王小明","created_at":"\(t(-1800))","expires_at":"\(t(84600))","max_uses":1,"lease_duration":"8h","uses":[{"name":"王小明","ip":"192.168.1.35","at":"\(t(-1700))"}]},
                   {"code":"a3","note":"测试组","created_at":"\(t(-60))","expires_at":"\(t(600000))","max_uses":0,"lease_duration":"today","require_approval":true}]}},
     {"id":"t2","name":"Grafana 监控面板","target":"10.0.2.30:3000","listen_port":33661,"whitelist":["192.168.1.0/24"],"enabled":true,
      "owner":"运维组","duration_minutes":0,"started_at":"\(t(-50000))","effective_idle_close_minutes":0,"idle_close_minutes":0,"first_connected_at":"\(t(-49000))","connected_sec":\(36120 + Int(dt)),
      "activity":{"in_use":true,"connected_sec":\(36120 + Int(dt))},
      "metrics":{"tunnel_id":"t2","status":"online","active_conns":2,"total_conns":342,"bytes_in":\(52039104 + tick * 300000),"bytes_out":2703180,"rate_in":0,"rate_out":0,"blocked_attempts":0}},
     {"id":"t3","name":"SSH 跳板机（测试环境）","target":"10.0.8.3:22","listen_port":33662,"whitelist":[],"enabled":true,
      "owner":"运维组","duration_minutes":0,"started_at":"\(t(-2100))","effective_idle_close_minutes":30,"idle_close_minutes":30,"first_connected_at":"\(t(-2000))","connected_sec":640,
      "activity":{"in_use":false,"idle_since":"\(t(-720))","auto_close_at":"\(t(1080))","connected_sec":640},
      "metrics":{"tunnel_id":"t3","status":"online","active_conns":0,"total_conns":4,"bytes_in":1983000,"bytes_out":204000,"rate_in":0,"rate_out":0,"blocked_attempts":0}},
     {"id":"g1a","name":"订单服务 (10.0.3.11)","target":"10.0.3.11:22","listen_port":33670,"whitelist":[],"enabled":true,"group_id":"g1",
      "owner":"运维组","started_at":"\(t(-1800))","effective_idle_close_minutes":30,"idle_close_minutes":30,"first_connected_at":"\(t(-1500))","connected_sec":\(1320 + Int(dt)),
      "activity":{"in_use":true,"connected_sec":\(1320 + Int(dt))},
      "metrics":{"tunnel_id":"g1a","status":"online","active_conns":1,"total_conns":9,"bytes_in":3203180,"bytes_out":18039104,"rate_in":0,"rate_out":0,"blocked_attempts":0}},
     {"id":"g1b","name":"订单服务 (10.0.3.12)","target":"10.0.3.12:22","listen_port":33671,"whitelist":[],"enabled":true,"group_id":"g1",
      "owner":"运维组","started_at":"\(t(-1800))","effective_idle_close_minutes":30,"idle_close_minutes":30,"first_connected_at":"\(t(-1400))","connected_sec":\(1100 + Int(dt)),
      "activity":{"in_use":true,"connected_sec":\(1100 + Int(dt))},
      "metrics":{"tunnel_id":"g1b","status":"online","active_conns":2,"total_conns":14,"bytes_in":2203180,"bytes_out":12039104,"rate_in":0,"rate_out":0,"blocked_attempts":0}},
     {"id":"g1c","name":"订单服务 (10.0.3.13)","target":"10.0.3.13:22","listen_port":33672,"whitelist":[],"enabled":true,"group_id":"g1",
      "owner":"运维组","started_at":"\(t(-1800))","effective_idle_close_minutes":30,"idle_close_minutes":30,"connected_sec":0,
      "activity":{"in_use":true,"connected_sec":0},
      "metrics":{"tunnel_id":"g1c","status":"online","active_conns":0,"total_conns":0,"bytes_in":0,"bytes_out":0,"rate_in":0,"rate_out":0,"blocked_attempts":0}},
     {"id":"t4","name":"Redis 缓存","target":"10.0.1.20:6379","listen_port":33663,"whitelist":[],"enabled":false,"last_duration_sec":1905,"duration_minutes":0,"effective_idle_close_minutes":30,"last_connected_sec":742,"close_reason":"闲置 30 分钟自动关闭",
      "historical_bytes_in":3145728,"historical_bytes_out":524288}
    ]
    """)
    vm.tunnels[0].rate_history = demoSeries(tick: tick, seed: 0.4, base: 26000, burst: 150000, upScale: 0.08)
    vm.tunnels[1].rate_history = demoSeries(tick: tick, seed: 2.1, base: 9000, burst: 30000, upScale: 0.15)
    vm.tunnels[2].rate_history = RateSeriesItem(end: Int64(Date().timeIntervalSince1970), step: 5,
        up: (0..<120).map { $0 < 30 ? demoRate($0, seed: 5, base: 300, burst: 2000) : 0 },
        down: (0..<120).map { $0 < 30 ? demoRate($0, seed: 6, base: 1500, burst: 9000) : 0 })
    for (i, seed) in [(3, 3.3), (4, 4.6)] {
        vm.tunnels[i].rate_history = demoSeries(tick: tick, seed: seed, base: 4000, burst: 26000, upScale: 0.3)
    }
    vm.tunnels[5].rate_history = RateSeriesItem(end: Int64(Date().timeIntervalSince1970), step: 5,
        up: Array(repeating: 0, count: 120), down: Array(repeating: 0, count: 120))
    let ga = vm.tunnels[3].rate_history!, gb = vm.tunnels[4].rate_history!
    vm.groups = decode([GroupItem].self, """
    [{"id":"g1","name":"订单服务","owner":"运维组","created_at":"\(t(-1800))","tunnel_ids":["g1a","g1b","g1c"],
      "metrics":{"group_id":"g1",
        "dynamic_leases":[{"ip":"192.168.1.42","expires_at":"\(t(27000))","name":"小王","invite":"gx1","duration":"8h","started_at":"\(t(-1500))"}],
        "pending":[{"id":"gr1","tunnel_id":"","group_id":"g1","name":"小李","ip":"192.168.1.57","invite":"gx2","at":"\(t(-40))","status":"pending"}],
        "invites":[{"code":"gx1","note":"小王","created_at":"\(t(-1700))","expires_at":"\(t(84000))","max_uses":1,"lease_duration":"8h","uses":[{"name":"小王","ip":"192.168.1.42","at":"\(t(-1600))"}]},
                   {"code":"gx2","note":"测试组","created_at":"\(t(-100))","expires_at":"\(t(86000))","max_uses":3,"lease_duration":"today","require_approval":true}]}}]
    """)
    vm.groups[0].rate_history = RateSeriesItem(end: ga.end, step: 5, up: zip(ga.up, gb.up).map { $0 + $1 }, down: zip(ga.down, gb.down).map { $0 + $1 })
    // 授权延长：赵六的访问 20 分钟前结束，刚申请延长；王五结束不到 2 小时，还可以延长
    vm.tunnels[0].renewals = decode([RenewalItem].self, """
    [{"tunnel_id":"t1","ip":"192.168.1.47","name":"赵六","invite":"ab1","ended_at":"\(t(-1200))","renew_before":"\(t(6000))",
      "request":{"id":"rn1","tunnel_id":"t1","name":"赵六","ip":"192.168.1.47","invite":"ab1","at":"\(t(-60))","status":"pending","renewal":true}},
     {"tunnel_id":"t1","ip":"192.168.1.36","name":"王五","invite":"ab2","ended_at":"\(t(-3000))","renew_before":"\(t(4200))"}]
    """)
    // 卡片与顶部统计的实时速率取曲线最后一个点
    var totalIn: Int64 = 0, totalOut: Int64 = 0
    for i in [0, 1, 3, 4] {
        let h = vm.tunnels[i].rate_history!
        vm.tunnels[i].metrics?.rate_in = h.up.last ?? 0
        vm.tunnels[i].metrics?.rate_out = h.down.last ?? 0
        totalIn += h.up.last ?? 0; totalOut += h.down.last ?? 0
    }
    let a = vm.tunnels[0].rate_history!, b = vm.tunnels[1].rate_history!
    vm.totalRateHistory = RateSeriesItem(end: a.end, step: 5, up: zip(a.up, b.up).map { $0 + $1 }, down: zip(a.down, b.down).map { $0 + $1 })
    vm.activeConns = decode([ActiveClientInfo].self, """
    [
     {"conn_id":"c1","tunnel_id":"t1","client_ip":"192.168.1.21:52889","connected_at":"\(t(-600))","bytes_in":416358,"bytes_out":4089446,"rate_in":632,"rate_out":16486},
     {"conn_id":"c2","tunnel_id":"t1","client_ip":"192.168.1.21:50395","connected_at":"\(t(-577))","bytes_in":406528,"bytes_out":3984588,"rate_in":1126,"rate_out":29798},
     {"conn_id":"c3","tunnel_id":"t1","client_ip":"192.168.1.21:53023","connected_at":"\(t(-526))","bytes_in":244213760,"bytes_out":633856,"rate_in":0,"rate_out":0},
     {"conn_id":"c4","tunnel_id":"t2","client_ip":"192.168.1.52:61001","connected_at":"\(t(-3000))","bytes_in":120000,"bytes_out":8800000,"rate_in":120,"rate_out":9100},
     {"conn_id":"c6","tunnel_id":"g1a","client_ip":"192.168.1.42:50211","connected_at":"\(t(-900))","bytes_in":320000,"bytes_out":1800000,"rate_in":900,"rate_out":5100},
     {"conn_id":"c7","tunnel_id":"g1b","client_ip":"192.168.1.42:50388","connected_at":"\(t(-600))","bytes_in":220000,"bytes_out":1200000,"rate_in":700,"rate_out":3900},
     {"conn_id":"c8","tunnel_id":"g1b","client_ip":"192.168.1.42:50390","connected_at":"\(t(-300))","bytes_in":20000,"bytes_out":90000,"rate_in":0,"rate_out":0},
     {"conn_id":"c5","tunnel_id":"t2","client_ip":"192.168.1.60:61201","connected_at":"\(t(-300))","bytes_in":42000,"bytes_out":1800000,"rate_in":80,"rate_out":3100}
    ]
    """)
    vm.previewHistory = decode([ClientHistoryRecord].self, """
    [
     {"id":"h1","tunnel_id":"t1","tunnel_name":"MySQL 数据库","port":33660,"client_ip":"192.168.1.21:51000","connected_at":"\(t(-5000))","disconnected_at":"\(t(-4000))","duration_sec":1000,"bytes_in":1288490188,"bytes_out":5368709},
     {"id":"h2","tunnel_id":"t1","tunnel_name":"MySQL 数据库","port":33660,"client_ip":"192.168.1.21:51001","connected_at":"\(t(-3000))","disconnected_at":"\(t(-2990))","duration_sec":10,"bytes_in":2048,"bytes_out":4096},
     {"id":"h3","tunnel_id":"t1","tunnel_name":"MySQL 数据库","port":33660,"client_ip":"192.168.1.35:40000","connected_at":"\(t(-90000))","disconnected_at":"\(t(-86000))","duration_sec":4000,"bytes_in":99999999,"bytes_out":1},
     {"id":"h4","tunnel_id":"g1a","tunnel_name":"订单服务 (10.0.3.11)","port":33670,"client_ip":"192.168.1.42:50100","connected_at":"\(t(-7000))","disconnected_at":"\(t(-6400))","duration_sec":600,"bytes_in":820000,"bytes_out":5200000},
     {"id":"h5","tunnel_id":"g1b","tunnel_name":"订单服务 (10.0.3.12)","port":33671,"client_ip":"192.168.1.42:50120","connected_at":"\(t(-6800))","disconnected_at":"\(t(-6000))","duration_sec":800,"bytes_in":420000,"bytes_out":3100000},
     {"id":"h6","tunnel_id":"g1a","tunnel_name":"订单服务 (10.0.3.11)","port":33670,"client_ip":"192.168.1.66:41022","connected_at":"\(t(-90000))","disconnected_at":"\(t(-88200))","duration_sec":1800,"bytes_in":1200000,"bytes_out":9400000}
    ]
    """)
    vm.totalActiveConns = 8; vm.totalHistoricalConns = 1656; vm.totalRateIn = totalIn; vm.totalRateOut = totalOut
    vm.totalTransferred = 60000000000; vm.totalBlockedAttempts = 12
    vm.runDiagnostics()
    return vm
}

/// 让离屏窗口按"前台激活"状态绘制控件（开关、分段控件显示强调色）
final class KeyWindow: NSWindow {
    override var isKeyWindow: Bool { true }
    override var isMainWindow: Bool { true }
    /// 渲染进程不会被激活，AppKit 控件（开关、分段控件）按这个私有属性决定是否画成灰色
    @objc(hasKeyAppearance) func alwaysHasKeyAppearance() -> Bool { true }
}

@MainActor
func render<V: View>(_ name: String, _ size: CGSize, dark: Bool = false, _ view: V) {
    if !only.isEmpty && !name.hasPrefix(only) { return }
    let root = view.frame(width: size.width, height: size.height, alignment: .topLeading)
        .background(Color(NSColor.windowBackgroundColor))
    let hosting = NSHostingView(rootView: root)
    hosting.frame = NSRect(origin: .zero, size: size)
    let window = KeyWindow(contentRect: hosting.frame, styleMask: [.titled], backing: .buffered, defer: false)
    window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
    hosting.appearance = window.appearance
    window.contentView = hosting
    for _ in 0..<6 {
        hosting.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.15))
    }
    guard let rep = hosting.bitmapImageRepForCachingDisplay(in: hosting.bounds) else { return }
    hosting.cacheDisplay(in: hosting.bounds, to: rep)
    try! rep.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: "\(outDir)/\(name).png"))
    print("rendered", name)
}

MainActor.assumeIsolated {
    let vm = makeVM()
    let t1 = vm.tunnels[0]
    let yes = Binding.constant(true)
    render("01-main-light", CGSize(width: 980, height: 720), ContentView(vm: vm))
    render("01c-search-hit", CGSize(width: 980, height: 560), ContentView(vm: vm, initialSearch: "sjk"))
    render("01d-search-none", CGSize(width: 980, height: 560), ContentView(vm: vm, initialSearch: "oracle", initialFilter: .running))
    render("02-main-dark", CGSize(width: 980, height: 720), dark: true, ContentView(vm: vm))
    render("03-clients-online", CGSize(width: 760, height: 600), ClientDetailsSheet(vm: vm, tunnel: t1, initialTab: 0, isPresented: yes))
    let vmSingle = makeVM()
    vmSingle.activeConns = vmSingle.activeConns.filter { $0.client_ip.hasPrefix("192.168.1.21:") }
    render("03b-clients-online-single", CGSize(width: 760, height: 600), ClientDetailsSheet(vm: vmSingle, tunnel: vmSingle.tunnels[0], initialTab: 0, isPresented: yes))
    render("03c-clients-online-min", CGSize(width: 720, height: 540), ClientDetailsSheet(vm: vmSingle, tunnel: vmSingle.tunnels[0], initialTab: 0, isPresented: yes))
    render("01b-main-min", CGSize(width: 920, height: 640), ContentView(vm: vm))
    render("04-clients-history", CGSize(width: 760, height: 600), ClientDetailsSheet(vm: vm, tunnel: t1, initialTab: 1, isPresented: yes))
    render("05-clients-auth", CGSize(width: 760, height: 600), ClientDetailsSheet(vm: vm, tunnel: t1, initialTab: 2, isPresented: yes))
    render("06-edit-new", CGSize(width: 520, height: 760), TunnelEditSheet(vm: vm, tunnel: nil, isPresented: yes))
    render("07-edit-existing", CGSize(width: 520, height: 760), TunnelEditSheet(vm: vm, tunnel: vm.tunnels[1], isPresented: yes))
    render("08-invite-form", CGSize(width: 560, height: 400), InviteSheet(vm: vm, tunnel: t1, autoGenerate: false, isPresented: yes))
    render("09-invite-result", CGSize(width: 560, height: 400), InviteSheet(vm: vm, tunnel: t1, autoGenerate: true, isPresented: yes))
    render("10-settings", CGSize(width: 400, height: 320), SettingsSheet(vm: vm, isPresented: yes))
    render("11-diagnostics", CGSize(width: 580, height: 460), DiagnosticsSheet(vm: vm, isPresented: yes))
    render("12-clients-auth-dark", CGSize(width: 760, height: 600), dark: true, ClientDetailsSheet(vm: vm, tunnel: t1, initialTab: 2, isPresented: yes))

    // README 截图
    let demo = makeDemoVM()
    render("readme-main-light", CGSize(width: 980, height: 1180), ContentView(vm: demo))
    render("readme-main-dark", CGSize(width: 980, height: 1180), dark: true, ContentView(vm: demo))
    render("readme-invite", CGSize(width: 600, height: 270), InviteSheet(vm: demo, tunnel: demo.tunnels[0], autoGenerate: true, isPresented: yes))
    render("readme-clients", CGSize(width: 760, height: 560), ClientDetailsSheet(vm: demo, tunnel: demo.tunnels[0], initialTab: 0, isPresented: yes))
    render("readme-edit", CGSize(width: 520, height: 570), TunnelEditSheet(vm: demo, tunnel: demo.tunnels[0], isPresented: yes))
    let g = demo.groups[0]
    render("readme-group", CGSize(width: 980, height: 330), GroupCardView(group: g, members: demo.members(of: g), visible: demo.members(of: g), vm: demo,
        onEdit: {}, onAddMember: {}, onInvite: {}, onViewClients: { _ in }, onEditMember: { _ in }, onInviteMember: { _ in }, onMemberClients: { _ in }).padding(20))
    render("group-card-dark", CGSize(width: 980, height: 330), dark: true, GroupCardView(group: g, members: demo.members(of: g), visible: demo.members(of: g), vm: demo,
        onEdit: {}, onAddMember: {}, onInvite: {}, onViewClients: { _ in }, onEditMember: { _ in }, onInviteMember: { _ in }, onMemberClients: { _ in }).padding(20))
    render("group-card-narrow", CGSize(width: 720, height: 360), GroupCardView(group: g, members: demo.members(of: g), visible: demo.members(of: g), vm: demo,
        onEdit: {}, onAddMember: {}, onInvite: {}, onViewClients: { _ in }, onEditMember: { _ in }, onInviteMember: { _ in }, onMemberClients: { _ in }).padding(20))
    render("group-add-batch", CGSize(width: 520, height: 900), TunnelEditSheet(vm: demo, tunnel: nil, isPresented: yes,
        draftName: "订单服务", draftRows: [("10.0.3.11", "22, 8080"), ("10.0.3.12", "22, 8080"), ("10.0.3.13", "22")]))
    render("group-add-single", CGSize(width: 520, height: 780), TunnelEditSheet(vm: demo, tunnel: nil, isPresented: yes,
        draftName: "MySQL 实习库", draftRows: [("10.0.1.16", "3306")]))
    render("group-add-member", CGSize(width: 520, height: 780), TunnelEditSheet(vm: demo, tunnel: nil, isPresented: yes, groupId: g.id))
    render("group-sheet-edit", CGSize(width: 520, height: 420), GroupEditSheet(vm: demo, group: g, isPresented: yes))
    render("group-invite", CGSize(width: 560, height: 300), InviteSheet(vm: demo, scope: .group(g.id), autoGenerate: true, isPresented: yes))
    render("group-access", CGSize(width: 760, height: 560), ClientDetailsSheet(vm: demo, scope: .group(g.id), initialTab: 2, isPresented: yes))
    render("group-online", CGSize(width: 760, height: 400), ClientDetailsSheet(vm: demo, scope: .group(g.id), initialTab: 0, isPresented: yes))
    render("group-online-addr", CGSize(width: 760, height: 460), ClientDetailsSheet(vm: demo, scope: .group(g.id), initialTab: 0, byAddress: true, isPresented: yes))
    render("group-history", CGSize(width: 760, height: 400), ClientDetailsSheet(vm: demo, scope: .group(g.id), initialTab: 1, isPresented: yes))
    render("group-history-addr", CGSize(width: 760, height: 520), ClientDetailsSheet(vm: demo, scope: .group(g.id), initialTab: 1, byAddress: true, isPresented: yes))
    render("group-edit-member", CGSize(width: 520, height: 800), TunnelEditSheet(vm: demo, tunnel: demo.tunnels[3], isPresented: yes))

    // 授权延长：申请延长、最近结束、已授权设备的「延长」
    render("readme-access", CGSize(width: 760, height: 800), ClientDetailsSheet(vm: demo, tunnel: demo.tunnels[0], initialTab: 2, isPresented: yes))
    render("renew-card", CGSize(width: 980, height: 220), TunnelCardView(tunnel: demo.tunnels[0], vm: demo, onEdit: {}, onViewClients: {}, onViewLeases: {}, onInvite: {}).padding(20))
    render("renew-invite-form", CGSize(width: 600, height: 400), InviteSheet(vm: demo, tunnel: demo.tunnels[0], autoGenerate: false, isPresented: yes))
    render("renew-custom", CGSize(width: 300, height: 90), CustomMinutesForm(actionTitle: "延长") { _ in })

    // README 动图逐帧：卡片随时间推移（每帧 = 5 秒）
    if only.hasPrefix("gif") {
        let frames = Int(ProcessInfo.processInfo.environment["FRAMES"] ?? "") ?? 40
        for i in 0..<frames {
            let fvm = makeDemoVM(tick: i)
            let card = TunnelCardView(tunnel: fvm.tunnels[0], vm: fvm, onEdit: {}, onViewClients: {}, onViewLeases: {}, onInvite: {})
                .padding(.horizontal, 20).padding(.vertical, 12)
            render(String(format: "gif-%03d", i), CGSize(width: 980, height: 192), card)
        }
    }
}

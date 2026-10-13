import SwiftUI
import AppKit
import CoreImage.CIFilterBuiltins
import Charts
import UserNotifications

// MARK: - Models

struct TunnelItem: Identifiable, Codable {
    var id: String
    var name: String
    var target: String
    var listen_port: Int
    var whitelist: [String]
    var enabled: Bool
    var owner: String?
    var duration_minutes: Int?
    var started_at: String?
    var expires_at: String?
    var historical_bytes_in: Int64?
    var historical_bytes_out: Int64?
    var last_duration_sec: Int64?
    var max_bandwidth_mb: Double?
    var auth_code: String?
    var notes: String?
    var metrics: TunnelMetrics?
    var idle_close_minutes: Int?
    var effective_idle_close_minutes: Int?
    var close_reason: String?
    var first_connected_at: String?
    var connected_sec: Int64?
    var last_connected_sec: Int64?
    var activity: TunnelActivityItem?
    var rate_history: RateSeriesItem?
    /// 所属通道组
    var group_id: String?
}

/// 近 10 分钟流量曲线：固定 120 个点（5 秒一个），最后一点为当前
struct RateSeriesItem: Codable {
    var end: Int64
    var step: Int
    var up: [Int64]
    var down: [Int64]

    var peak: Int64 { max(up.max() ?? 0, down.max() ?? 0) }
}

/// 通道当前使用情况：是否在用、闲置起点、预计自动关闭时间、实际连接时长
struct TunnelActivityItem: Codable {
    var in_use: Bool
    var idle_since: String?
    var auto_close_at: String?
    var connected_sec: Int64
}

struct TunnelMetrics: Codable {
    var tunnel_id: String?
    var listen_port: Int?
    var active_conns: Int?
    var total_conns: Int64?
    var bytes_in: Int64?
    var bytes_out: Int64?
    var rate_in: Int64?
    var rate_out: Int64?
    var blocked_attempts: Int64?
    var status: String?
    var error: String?
    var dynamic_whitelist: [String]?
    var dynamic_leases: [DynamicLeaseItem]?
    var invites: [InviteItem]?
    var pending: [AccessRequestItem]?
}

struct DynamicLeaseItem: Identifiable, Codable {
    var id: String { ip }
    var ip: String
    var expires_at: String
    var name: String?
    var invite: String?
    var duration: String?
    /// 已授权但尚未首次连接：计时未开始，expires_at 为首次连接截止时间
    var awaiting_connect: Bool?
    var started_at: String?
}

struct InviteUseItem: Codable, Hashable {
    var name: String
    var ip: String
    var at: String
}

struct InviteItem: Identifiable, Codable {
    var id: String { code }
    var code: String
    var note: String?
    var created_at: String
    var expires_at: String
    var max_uses: Int
    /// 通过该邀请授权后可访问多久（每个邀请单独设置）
    var lease_duration: String?
    /// 通过该邀请提交的申请是否需要审批
    var require_approval: Bool?
    var uses: [InviteUseItem]?
}

struct AccessRequestItem: Identifiable, Codable {
    var id: String
    var tunnel_id: String
    var group_id: String?
    var name: String
    var ip: String
    var invite: String?
    var at: String
    var status: String
}

/// 通道组：组内通道共用邀请、授权与审批，闲置计时联动
struct GroupItem: Identifiable, Codable {
    var id: String
    var name: String
    var owner: String?
    var created_at: String?
    /// 成员通道，按监听端口排序
    var tunnel_ids: [String]
    var metrics: GroupMetricsItem?
    var rate_history: RateSeriesItem?
}

struct GroupMetricsItem: Codable {
    var group_id: String?
    var dynamic_leases: [DynamicLeaseItem]?
    var invites: [InviteItem]?
    var pending: [AccessRequestItem]?
}

struct GroupPreviewMember: Codable, Identifiable {
    var id: String { target }
    var name: String
    var target: String
    var listen_port: Int
}

struct GroupPreviewResp: Codable {
    var members: [GroupPreviewMember]
}

/// 授权的作用范围：单个通道，或整个通道组
enum AccessScope: Hashable {
    case tunnel(String)
    case group(String)

    var key: String {
        switch self {
        case .tunnel(let id): return "t:\(id)"
        case .group(let id): return "g:\(id)"
        }
    }

    /// 授权相关接口的前缀（/invites、/requests、/leases 挂在其下）
    var apiPath: String {
        switch self {
        case .tunnel(let id): return "/api/tunnels/\(id)"
        case .group(let id): return "/api/groups/\(id)"
        }
    }

    var historyQuery: String {
        switch self {
        case .tunnel(let id): return "tunnel_id=\(id)"
        case .group(let id): return "group_id=\(id)"
        }
    }

    var isGroup: Bool {
        if case .group = self { return true }
        return false
    }
}

struct InviteShareResp: Codable {
    var invite: InviteItem
    var link: String
    var share_text: String
}

struct ConnectionsResp: Codable {
    var active_conns: [ActiveClientInfo]
    var security_events: [SecurityEventItem]
}

struct ActiveClientInfo: Identifiable, Codable {
    var id: String { conn_id }
    var conn_id: String
    var tunnel_id: String
    var client_ip: String
    var connected_at: String
    var bytes_in: Int64
    var bytes_out: Int64
    var rate_in: Int64
    var rate_out: Int64
}

struct ClientHistoryRecord: Identifiable, Codable {
    var id: String
    var tunnel_id: String
    var tunnel_name: String
    var port: Int
    var client_ip: String
    var connected_at: String
    var disconnected_at: String
    var duration_sec: Int64
    var bytes_in: Int64
    var bytes_out: Int64
}

struct SecurityEventItem: Identifiable, Codable {
    var id: String
    var tunnel_id: String
    var port: Int
    var client_ip: String
    var timestamp: String
    var reason: String
}

struct ServerStatusResp: Codable {
    var connected: Bool
    var server_addr: String
    var port_range: String
    var public_host: String?
    var last_error: String?
    var invite_base: String?
    var total_rate_history: RateSeriesItem?
}

struct ProbeResp: Codable {
    var target: String
    var reachable: Bool
    var rtt: String
    var error: String?
}

struct RecommendPortResp: Codable {
    var recommended_port: Int
}

struct SettingsResp: Codable {
    var server_addr: String
    var token: String
    var public_host: String?
}

// Diagnostic Item Model
enum DiagnosticStatus {
    case ok
    case warning
    case error
    case checking
}

struct DiagnosticItem: Identifiable {
    let id = UUID()
    let title: String
    let desc: String
    let status: DiagnosticStatus
    let statusText: String
    let actionTitle: String?
    let action: (() -> Void)?
}

// MARK: - Helper Formatting

func formatBytes(_ bytes: Int64) -> String {
    if bytes <= 0 { return "0 B" }
    let d = Double(bytes)
    let k = 1024.0
    let sizes = ["B", "KB", "MB", "GB", "TB"]
    let i = Int(log(d) / log(k))
    let clampedI = min(i, sizes.count - 1)
    let val = d / pow(k, Double(clampedI))
    return String(format: "%.1f %@", val, sizes[clampedI])
}

func formatRate(_ bytesPerSec: Int64) -> String {
    return "\(formatBytes(bytesPerSec))/s"
}

func parseISO8601(_ str: String?) -> Date? {
    guard let s = str, !s.isEmpty else { return nil }
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = formatter.date(from: s) { return d }
    formatter.formatOptions = [.withInternetDateTime]
    return formatter.date(from: s)
}

func formatRemainingTime(expiresAt: String?) -> (text: String, isExpiringSoon: Bool) {
    guard let exp = parseISO8601(expiresAt) else {
        return ("持续开启 (无限制)", false)
    }
    let diff = exp.timeIntervalSince(Date())
    if diff <= 0 {
        return ("已自动到期停止", true)
    }
    let totalSec = Int(diff)
    let hours = totalSec / 3600
    let mins = (totalSec % 3600) / 60
    let secs = totalSec % 60
    let isSoon = diff < 300 // less than 5 min
    if hours > 0 {
        return (String(format: "剩余 %02d:%02d:%02d", hours, mins, secs), isSoon)
    } else {
        return (String(format: "剩余 %02d:%02d", mins, secs), isSoon)
    }
}

func formatElapsedTime(startedAt: String?) -> String {
    guard let start = parseISO8601(startedAt) else { return "-" }
    let diff = Date().timeIntervalSince(start)
    if diff < 0 { return "00:00" }
    let totalSec = Int(diff)
    let hours = totalSec / 3600
    let mins = (totalSec % 3600) / 60
    let secs = totalSec % 60
    if hours > 0 {
        return String(format: "%02d:%02d:%02d", hours, mins, secs)
    } else {
        return String(format: "%02d:%02d", mins, secs)
    }
}

func formatSecondsToDuration(_ totalSec: Int64) -> String {
    if totalSec <= 0 { return "0秒" }
    let hours = totalSec / 3600
    let mins = (totalSec % 3600) / 60
    let secs = totalSec % 60
    if hours > 0 {
        return "\(hours)小时\(mins)分"
    } else if mins > 0 {
        return "\(mins)分\(secs)秒"
    } else {
        return "\(secs)秒"
    }
}

/// 生成二维码图片
func makeQRCode(_ text: String, size: CGFloat = 180) -> NSImage? {
    let filter = CIFilter.qrCodeGenerator()
    filter.message = Data(text.utf8)
    filter.correctionLevel = "M"
    guard let output = filter.outputImage else { return nil }
    let scale = size / output.extent.width
    let scaled = output.transformed(by: CGAffineTransform(scaleX: scale, y: scale))
    let rep = NSCIImageRep(ciImage: scaled)
    let image = NSImage(size: rep.size)
    image.addRepresentation(rep)
    return image
}

let leaseDurationOptions: [(value: String, title: String)] = [
    ("1h", "1 小时"), ("2h", "2 小时"), ("8h", "8 小时"), ("today", "当天有效"), ("tunnel", "通道开启期间")
]

func leaseDurationTitle(_ v: String?) -> String {
    leaseDurationOptions.first { $0.value == (v ?? "2h") }?.title ?? "2 小时"
}

func copyToPasteboard(_ text: String) {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
}

// MARK: - Shared UI Components

/// 列表行统一卡片样式（tint 用于需要强调的行，如待审批）
struct RowCard: ViewModifier {
    var tint: Color? = nil
    func body(content: Content) -> some View {
        content
            .background(RoundedRectangle(cornerRadius: 8).fill(tint.map { $0.opacity(0.08) } ?? Color.primary.opacity(0.035)))
            .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(tint.map { $0.opacity(0.35) } ?? Color.primary.opacity(0.08), lineWidth: 1))
    }
}

extension View {
    func rowCard(tint: Color? = nil) -> some View { modifier(RowCard(tint: tint)) }
}

/// 流式布局：一行放不下时自动换行（时间口径与状态标签共用一行，空间不够再折行）
struct FlowLayout: Layout {
    var spacing: CGFloat = 14
    var lineSpacing: CGFloat = 6

    private func rows(_ subviews: Subviews, maxWidth: CGFloat) -> [[(Int, CGSize)]] {
        var rows: [[(Int, CGSize)]] = [[]]
        var x: CGFloat = 0
        for (i, v) in subviews.enumerated() {
            let size = v.sizeThatFits(.unspecified)
            let needed = rows[rows.count - 1].isEmpty ? size.width : x + spacing + size.width
            if !rows[rows.count - 1].isEmpty && needed > maxWidth {
                rows.append([])
                x = size.width
            } else {
                x = needed
            }
            rows[rows.count - 1].append((i, size))
        }
        return rows
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        let rs = rows(subviews, maxWidth: maxWidth)
        var width: CGFloat = 0
        var height: CGFloat = 0
        for (n, row) in rs.enumerated() where !row.isEmpty {
            let w = row.map { $0.1.width }.reduce(0, +) + spacing * CGFloat(row.count - 1)
            width = max(width, w)
            height += (row.map { $0.1.height }.max() ?? 0) + (n > 0 ? lineSpacing : 0)
        }
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in rows(subviews, maxWidth: bounds.width) where !row.isEmpty {
            let h = row.map { $0.1.height }.max() ?? 0
            var x = bounds.minX
            for (i, size) in row {
                subviews[i].place(at: CGPoint(x: x, y: y + (h - size.height) / 2), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += h + lineSpacing
        }
    }
}

let downColor = Color.blue
let upColor = Color.teal

/// 流量曲线（面积 + 平滑线），纵轴随峰值自动缩放
struct RateSparkline: View {
    let series: RateSeriesItem?
    var height: CGFloat = 40
    var showsArea: Bool = true

    struct Point: Identifiable {
        let id: String
        let i: Int
        let v: Double
        let dir: String
    }

    var points: [Point] {
        guard let s = series else { return [] }
        var out: [Point] = []
        for (i, v) in s.down.enumerated() { out.append(Point(id: "d\(i)", i: i, v: Double(v), dir: "down")) }
        for (i, v) in s.up.enumerated() { out.append(Point(id: "u\(i)", i: i, v: Double(v), dir: "up")) }
        return out
    }

    var body: some View {
        let peak = Double(series?.peak ?? 0)
        let count = max((series?.down.count ?? 120) - 1, 1)
        Chart(points) { p in
            if showsArea {
                AreaMark(x: .value("t", p.i), y: .value("rate", p.v), series: .value("dir", p.dir), stacking: .unstacked)
                    .interpolationMethod(.monotone)
                    .foregroundStyle(LinearGradient(
                        colors: [(p.dir == "down" ? downColor : upColor).opacity(0.30), (p.dir == "down" ? downColor : upColor).opacity(0.02)],
                        startPoint: .top, endPoint: .bottom))
            }
            LineMark(x: .value("t", p.i), y: .value("rate", p.v), series: .value("dir", p.dir))
                .interpolationMethod(.monotone)
                .lineStyle(StrokeStyle(lineWidth: 1.4, lineCap: .round, lineJoin: .round))
                .foregroundStyle(p.dir == "down" ? downColor : upColor)
        }
        .chartXScale(domain: 0...count)
        .chartYScale(domain: 0...max(peak * 1.18, 1024))
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartLegend(.hidden)
        .frame(height: height)
    }
}

/// 当前速率图例：● ↓ 下行  ● ↑ 上行
struct RateLegend: View {
    let down: Int64
    let up: Int64

    var body: some View {
        HStack(spacing: 10) {
            item(downColor, "↓ \(formatRate(down))")
            item(upColor, "↑ \(formatRate(up))")
        }
        .lineLimit(1)
    }

    func item(_ color: Color, _ text: String) -> some View {
        HStack(spacing: 4) {
            Circle().fill(color).frame(width: 6, height: 6)
            Text(verbatim: text)
                .font(.system(size: 11, weight: .medium, design: .monospaced))
        }
    }
}

/// 卡片内的流量曲线：不加背景，直接融入卡片；底部一条淡基线 + 时间范围与峰值
struct InlineTrafficChart: View {
    let series: RateSeriesItem?

    var body: some View {
        let peak = series?.peak ?? 0
        VStack(spacing: 3) {
            ZStack(alignment: .bottom) {
                Rectangle()
                    .fill(Color.primary.opacity(0.10))
                    .frame(height: 1)
                if peak > 0 {
                    RateSparkline(series: series, height: 44)
                } else {
                    Text("近 10 分钟无流量")
                        .font(.system(size: 10))
                        .foregroundColor(.secondary)
                        .frame(maxHeight: .infinity)
                }
            }
            .frame(height: 44)
            HStack {
                Text("近 10 分钟")
                Spacer(minLength: 0)
                if peak > 0 {
                    Text(verbatim: "峰值 \(formatRate(peak))")
                }
            }
            .font(.system(size: 10))
            .foregroundColor(.secondary)
            .monospacedDigit()
            .lineLimit(1)
        }
    }
}

struct CardWidthKey: PreferenceKey {
    static var defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
}

/// 小号胶囊标签
struct TagView: View {
    let text: String
    var color: Color = .secondary

    init(_ text: String, color: Color = .secondary) {
        self.text = text
        self.color = color
    }

    var body: some View {
        Text(verbatim: text)
            .font(.system(size: 10, weight: .medium))
            .lineLimit(1)
            .padding(.horizontal, 5)
            .padding(.vertical, 1.5)
            .background(color.opacity(0.13))
            .foregroundColor(color == .secondary ? .primary.opacity(0.75) : color)
            .cornerRadius(4)
    }
}

/// 上下行速率：固定宽度 + 等宽数字，避免数值变化时挤压左侧内容导致换行或跳动
struct RateText: View {
    let up: Int64
    let down: Int64
    var size: CGFloat = 11

    var body: some View {
        Text(verbatim: "↑ \(formatRate(up))  ↓ \(formatRate(down))")
            .font(.system(size: size, design: .monospaced))
            .foregroundColor(.secondary)
            .lineLimit(1)
            .frame(width: size * 18.5, alignment: .trailing)
    }
}

/// 统一的选项按钮（开启时长、限速、授权时长、邀请选项）
struct ChipLabel: View {
    let title: String
    let selected: Bool
    var tint: Color = .accentColor

    var body: some View {
        Text(verbatim: title)
            .font(.system(size: 11, weight: selected ? .semibold : .regular))
            .lineLimit(1)
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(selected ? tint.opacity(0.15) : Color(NSColor.controlBackgroundColor))
            .foregroundColor(selected ? tint : .primary)
            .cornerRadius(4)
            .overlay(
                RoundedRectangle(cornerRadius: 4)
                    .stroke(selected ? tint.opacity(0.8) : Color(NSColor.separatorColor), lineWidth: 1)
            )
    }
}

struct StringChip: View {
    let title: String
    let value: String
    @Binding var selection: String

    var body: some View {
        Button(action: { selection = value }) {
            ChipLabel(title: title, selected: selection == value)
        }
        .buttonStyle(.borderless)
    }
}

// MARK: - Tunnel Search

/// 通道搜索：多个关键字（空格分隔）需全部命中；不区分大小写；中文支持全拼与拼音首字母
@MainActor
enum TunnelSearch {
    private static var pinyinCache: [String: (full: String, initials: String)] = [:]

    /// "MySQL 数据库" -> ("mysqlshujuku", "msjk")
    static func pinyin(_ text: String) -> (full: String, initials: String) {
        if let hit = pinyinCache[text] { return hit }
        let latin = (text.applyingTransform(.toLatin, reverse: false) ?? text)
            .applyingTransform(.stripDiacritics, reverse: false)?.lowercased() ?? text.lowercased()
        let words = latin.split(whereSeparator: { !$0.isLetter && !$0.isNumber })
        let result = (words.joined(), String(words.compactMap { $0.first }))
        if pinyinCache.count > 2000 { pinyinCache.removeAll() }
        pinyinCache[text] = result
        return result
    }

    static func fields(_ t: TunnelItem, publicHost: String) -> [String] {
        var f: [String] = [t.name, t.target, "\(t.listen_port)", "\(publicHost):\(t.listen_port)",
                           t.owner ?? "", t.notes ?? "", t.whitelist.joined(separator: " ")]
        f.append(t.enabled ? "运行中 开启" : "已暂停 关闭")
        f.append(t.whitelist.isEmpty ? "仅授权链接" : (t.whitelist.contains("*") ? "全部放通" : "固定ip 白名单"))
        f += (t.metrics?.dynamic_leases ?? []).compactMap { $0.name }
        f += (t.metrics?.dynamic_leases ?? []).map { $0.ip }
        f += (t.metrics?.invites ?? []).compactMap { $0.note }
        return f.filter { !$0.isEmpty }
    }

    static func matches(_ t: TunnelItem, query: String, publicHost: String) -> Bool {
        let tokens = query.lowercased().split(whereSeparator: { $0.isWhitespace }).map(String.init)
        if tokens.isEmpty { return true }
        let fs = fields(t, publicHost: publicHost)
        let plain = fs.joined(separator: " ").lowercased()
        // 拼音只对可能含中文的字段计算（名称、责任人、备注、授权人姓名）
        let py = ([t.name, t.owner ?? "", t.notes ?? ""] + (t.metrics?.dynamic_leases ?? []).compactMap { $0.name })
            .filter { !$0.isEmpty }
            .map { pinyin($0) }
        return tokens.allSatisfy { tok in
            plain.contains(tok) || py.contains { $0.full.contains(tok) || $0.initials.contains(tok) }
        }
    }
}

extension TunnelSearch {
    /// 任意文本（如通道组名称、责任人）是否命中全部关键字
    static func matches(texts: [String], query: String) -> Bool {
        let tokens = query.lowercased().split(whereSeparator: { $0.isWhitespace }).map(String.init)
        if tokens.isEmpty { return true }
        let plain = texts.joined(separator: " ").lowercased()
        let py = texts.filter { !$0.isEmpty }.map { pinyin($0) }
        return tokens.allSatisfy { tok in
            plain.contains(tok) || py.contains { $0.full.contains(tok) || $0.initials.contains(tok) }
        }
    }
}

enum TunnelStatusFilter: String, CaseIterable {
    case all = "全部", running = "运行中", paused = "已暂停"

    func accepts(_ t: TunnelItem) -> Bool {
        switch self {
        case .all: return true
        case .running: return t.enabled
        case .paused: return !t.enabled
        }
    }
}

/// 从 "ip:port" / "[ipv6]:port" 中取出 IP
func hostOf(_ addr: String) -> String {
    if addr.hasPrefix("["), let end = addr.firstIndex(of: "]") {
        return String(addr[addr.index(after: addr.startIndex)..<end])
    }
    if addr.filter({ $0 == ":" }).count == 1, let idx = addr.lastIndex(of: ":") {
        return String(addr[..<idx])
    }
    return addr
}

func portOf(_ addr: String) -> String {
    guard let idx = addr.lastIndex(of: ":"), addr.filter({ $0 == ":" }).count == 1 || addr.hasPrefix("[") else { return "" }
    return String(addr[addr.index(after: idx)...])
}

/// 同一 IP 的在线连接合并展示
struct ClientIPGroup: Identifiable {
    let ip: String
    let conns: [ActiveClientInfo]
    var id: String { ip }
    var bytesIn: Int64 { conns.reduce(0) { $0 + $1.bytes_in } }
    var bytesOut: Int64 { conns.reduce(0) { $0 + $1.bytes_out } }
    var rateIn: Int64 { conns.reduce(0) { $0 + $1.rate_in } }
    var rateOut: Int64 { conns.reduce(0) { $0 + $1.rate_out } }
    var firstConnectedAt: String? { conns.map { $0.connected_at }.min() }
}

/// 距现在的秒数（正数表示过去）
func secondsSince(_ str: String?) -> Int64 {
    guard let d = parseISO8601(str) else { return 0 }
    return Int64(Date().timeIntervalSince(d))
}

let idleCloseOptions: [(Int, String)] = [(15, "15 分钟"), (30, "30 分钟"), (60, "1 小时"), (120, "2 小时"), (0, "不自动关闭")]

func idleCloseTitle(_ minutes: Int) -> String {
    idleCloseOptions.first { $0.0 == minutes }?.1 ?? "\(minutes) 分钟"
}

/// 今天显示 HH:mm:ss，否则显示 MM-dd HH:mm
func formatDateTimeShort(_ str: String?) -> String {
    guard let d = parseISO8601(str) else { return "-" }
    let df = DateFormatter()
    df.dateFormat = Calendar.current.isDateInToday(d) ? "HH:mm:ss" : "MM-dd HH:mm"
    return df.string(from: d)
}

/// 同一 IP 的历史连接合并展示
struct HistoryIPGroup: Identifiable {
    let ip: String
    let records: [ClientHistoryRecord] // 按接入时间倒序
    var id: String { ip }
    var bytesIn: Int64 { records.reduce(0) { $0 + $1.bytes_in } }
    var bytesOut: Int64 { records.reduce(0) { $0 + $1.bytes_out } }
    var totalDuration: Int64 { records.reduce(0) { $0 + $1.duration_sec } }
    var lastSeen: String? { records.map { $0.disconnected_at }.max() }
    var firstSeen: String? { records.map { $0.connected_at }.min() }
}

func formatTimeHHmm(_ str: String?) -> String {
    guard let d = parseISO8601(str) else { return "-" }
    let df = DateFormatter()
    df.dateFormat = "HH:mm:ss"
    return df.string(from: d)
}

// MARK: - ViewModel

@MainActor
class PortRelayViewModel: ObservableObject {
    static let shared = PortRelayViewModel()
    weak var appDelegate: AppDelegate?

    @Published var isConnected: Bool = false
    @Published var agentReachable: Bool = true
    @Published var lastError: String = ""
    @Published var serverAddr: String = ""
    /// 访问者连接通道时使用的网关地址（设置中可修改）
    @Published var publicHost: String = ""
    /// 邀请链接前缀 http://<对外访问地址>:<控制端口>/i/
    @Published var inviteBase: String = ""
    /// 所有通道合计的近 10 分钟流量曲线
    @Published var totalRateHistory: RateSeriesItem? = nil
    /// 已提醒过的审批申请，避免重复通知
    private var notifiedRequests: Set<String> = []

    /// 所有待审批申请（单个通道的与通道组的）
    var pendingRequests: [(title: String, scope: AccessScope, request: AccessRequestItem)] {
        tunnels.flatMap { t in (t.metrics?.pending ?? []).map { (t.name, AccessScope.tunnel(t.id), $0) } }
            + groups.flatMap { g in (g.metrics?.pending ?? []).map { (g.name, AccessScope.group(g.id), $0) } }
    }

    // MARK: 通道组与授权范围

    func members(of g: GroupItem) -> [TunnelItem] {
        g.tunnel_ids.compactMap { id in tunnels.first { $0.id == id } }
    }

    func group(_ id: String?) -> GroupItem? {
        guard let id = id, !id.isEmpty else { return nil }
        return groups.first { $0.id == id }
    }

    func scopeTunnels(_ s: AccessScope) -> [TunnelItem] {
        switch s {
        case .tunnel(let id): return tunnels.filter { $0.id == id }
        case .group(let id): return group(id).map { members(of: $0) } ?? []
        }
    }

    func scopeTitle(_ s: AccessScope) -> String {
        switch s {
        case .tunnel(let id): return tunnels.first { $0.id == id }?.name ?? ""
        case .group(let id): return group(id)?.name ?? "通道组"
        }
    }

    /// 通道已开启，或通道组内至少有一个通道开启
    func scopeEnabled(_ s: AccessScope) -> Bool {
        scopeTunnels(s).contains { $0.enabled }
    }

    func scopeLeases(_ s: AccessScope) -> [DynamicLeaseItem] {
        switch s {
        case .tunnel(let id): return tunnels.first { $0.id == id }?.metrics?.dynamic_leases ?? []
        case .group(let id): return group(id)?.metrics?.dynamic_leases ?? []
        }
    }

    func scopeInvites(_ s: AccessScope) -> [InviteItem] {
        switch s {
        case .tunnel(let id): return tunnels.first { $0.id == id }?.metrics?.invites ?? []
        case .group(let id): return group(id)?.metrics?.invites ?? []
        }
    }

    func scopePending(_ s: AccessScope) -> [AccessRequestItem] {
        switch s {
        case .tunnel(let id): return tunnels.first { $0.id == id }?.metrics?.pending ?? []
        case .group(let id): return group(id)?.metrics?.pending ?? []
        }
    }

    /// 服务器控制端口（网页授权链接使用），取自控制地址
    var controlPort: String {
        if let idx = serverAddr.lastIndex(of: ":") {
            let port = serverAddr[serverAddr.index(after: idx)...]
            if !port.isEmpty { return String(port) }
        }
        return "33000"
    }
    @Published var tunnels: [TunnelItem] = []
    @Published var groups: [GroupItem] = []
    @Published var activeConns: [ActiveClientInfo] = []
    @Published var securityEvents: [SecurityEventItem] = []
    
    @Published var totalActiveConns: Int = 0
    @Published var totalHistoricalConns: Int64 = 0
    @Published var totalRateIn: Int64 = 0
    @Published var totalRateOut: Int64 = 0
    @Published var totalTransferred: Int64 = 0
    @Published var totalBlockedAttempts: Int64 = 0

    // Diagnostics
    @Published var diagnostics: [DiagnosticItem] = []
    @Published var isRunningDiagnostics: Bool = false

    private var timer: Timer?
    private var agentProcess: Process?
    private var agentFailures: Int = 0
    private var lastAgentLaunch: Date = .distantPast
    private var lastMenuSignature: String = ""

    /// preview = true 时不启动后台引擎、不轮询、不请求通知权限（用于界面预览与离屏渲染检查）
    /// 预览模式：网络调用返回下面的模拟数据
    private(set) var isPreview = false
    var previewHistory: [ClientHistoryRecord] = []

    init(preview: Bool = false) {
        isPreview = preview
        guard !preview else { return }
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
        startAgentIfNeeded()
        startPolling()
        runDiagnostics()
        setupSleepWakeNotifications()
    }

    private func setupSleepWakeNotifications() {
        NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.handleSystemWake()
            }
        }
        NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.screensDidWakeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.handleSystemWake()
            }
        }
    }

    func handleSystemWake() {
        Task {
            // 唤醒时只在会话失效时重连；屏幕唤醒等场景不再切断所有活跃连接
            await triggerReconnect(force: false)
            await refreshData()
            runDiagnostics()
        }
    }

    /// 彻底退出：通知后台引擎结束（包括不是由本 App 拉起的实例）
    func shutdownAgent() async {
        if let url = URL(string: "http://127.0.0.1:33080/api/shutdown") {
            var req = URLRequest(url: url)
            req.httpMethod = "POST"
            req.timeoutInterval = 2
            _ = try? await URLSession.shared.data(for: req)
        }
        if let p = agentProcess, p.isRunning {
            p.terminate()
        }
    }

    func triggerReconnect(force: Bool = true) async {
        let endpoint = force ? "http://127.0.0.1:33080/api/reconnect" : "http://127.0.0.1:33080/api/reconnect?mode=check"
        guard let url = URL(string: endpoint) else { return }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        _ = try? await URLSession.shared.data(for: req)
    }

    func startAgentIfNeeded() {
        let url = URL(string: "http://127.0.0.1:33080/api/status")!
        var req = URLRequest(url: url)
        req.timeoutInterval = 0.5
        URLSession.shared.dataTask(with: req) { [weak self] _, _, err in
            if err != nil {
                Task { @MainActor in
                    self?.launchAgentProcess()
                }
            }
        }.resume()
    }

    private func launchAgentProcess() {
        if let p = agentProcess, p.isRunning { return }
        lastAgentLaunch = Date()
        let bundleURL = Bundle.main.bundleURL
        let candidatePaths = [
            bundleURL.appendingPathComponent("Contents/Resources/portrelay-agent").path,
            bundleURL.deletingLastPathComponent().appendingPathComponent("bin/portrelay-agent").path
        ]

        var execPath: String?
        for path in candidatePaths {
            if FileManager.default.fileExists(atPath: path) {
                execPath = path
                break
            }
        }

        guard let agentPath = execPath else { return }

        let p = Process()
        p.executableURL = URL(fileURLWithPath: agentPath)
        p.arguments = ["-no-browser"]
        try? p.run()
        self.agentProcess = p
    }

    func startPolling() {
        timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            Task { @MainActor in
                await self?.refreshData()
            }
        }
        Task {
            await refreshData()
        }
    }

    func refreshData() async {
        await fetchStatus()
        await fetchTunnels()
        await fetchGroups()
        await fetchConnections()
        let onlineCount = tunnels.filter { $0.enabled }.count
        appDelegate?.updateStatusItemTitle(activeCount: onlineCount)
    }

    /// 新的审批申请弹出系统通知
    private func notifyNewRequests() {
        for (title, _, r) in pendingRequests where !notifiedRequests.contains(r.id) {
            notifiedRequests.insert(r.id)
            let content = UNMutableNotificationContent()
            content.title = "访问申请：\(title)"
            content.body = "\(r.name)（\(r.ip)）申请访问，打开 PortRelay 菜单栏或授权管理进行审批"
            content.sound = .default
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: r.id, content: content, trigger: nil))
        }
    }

    /// 菜单内容有变化时才需要重建（避免每秒重建打断已展开的菜单）
    func menuNeedsRebuild() -> Bool {
        let sig = "\(isConnected)|" + tunnels.map { "\($0.id):\($0.name):\($0.listen_port):\($0.enabled)" }.joined(separator: ",")
            + "|" + groups.map { "\($0.id):\($0.name)" }.joined(separator: ",")
            + "|" + pendingRequests.map { $0.request.id }.joined(separator: ",")
        if sig == lastMenuSignature { return false }
        lastMenuSignature = sig
        return true
    }

    func fetchStatus() async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/status") else { return }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            if let resp = try? JSONDecoder().decode(ServerStatusResp.self, from: data) {
                let wasConnected = self.isConnected
                self.isConnected = resp.connected
                self.serverAddr = resp.server_addr
                if let host = resp.public_host, !host.isEmpty { self.publicHost = host }
                self.inviteBase = resp.invite_base ?? ""
                self.totalRateHistory = resp.total_rate_history
                self.lastError = resp.last_error ?? ""
                self.agentReachable = true
                self.agentFailures = 0
                if wasConnected != resp.connected {
                    runDiagnostics()
                }
            }
        } catch {
            let wasConnected = self.isConnected
            self.isConnected = false
            self.agentReachable = false
            self.lastError = "本地后台引擎 (portrelay-agent) 未响应"
            if wasConnected { runDiagnostics() }
            // 后台引擎崩溃或被结束：连续失败后自动拉起（10 秒内最多一次）
            agentFailures += 1
            if agentFailures >= 3 && Date().timeIntervalSince(lastAgentLaunch) > 10 {
                launchAgentProcess()
            }
        }
    }

    func fetchTunnels() async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/tunnels") else { return }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            let items = try JSONDecoder().decode([TunnelItem].self, from: data)
            self.tunnels = items
            
            var aConns = 0
            var hConns: Int64 = 0
            var rIn: Int64 = 0
            var rOut: Int64 = 0
            var bytes: Int64 = 0
            var blocked: Int64 = 0

            for t in items {
                let histBytes = (t.historical_bytes_in ?? 0) + (t.historical_bytes_out ?? 0)
                bytes += histBytes
                if let m = t.metrics {
                    aConns += (m.active_conns ?? 0)
                    hConns += (m.total_conns ?? 0)
                    rIn += (m.rate_in ?? 0)
                    rOut += (m.rate_out ?? 0)
                    bytes += ((m.bytes_in ?? 0) + (m.bytes_out ?? 0))
                    blocked += (m.blocked_attempts ?? 0)
                }
            }

            self.totalActiveConns = aConns
            self.totalHistoricalConns = hConns
            self.totalRateIn = rIn
            self.totalRateOut = rOut
            self.totalTransferred = bytes
            self.totalBlockedAttempts = blocked
            notifyNewRequests()
        } catch {
            // 拿不到最新数据时不能继续展示旧的“运行中”和速率
            self.tunnels = self.tunnels.map { var t = $0; t.metrics = nil; return t }
            self.activeConns = []
            self.totalActiveConns = 0
            self.totalRateIn = 0
            self.totalRateOut = 0
        }
    }

    func fetchGroups() async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/groups") else { return }
        if let (data, _) = try? await URLSession.shared.data(from: url),
           let items = try? JSONDecoder().decode([GroupItem].self, from: data) {
            self.groups = items
            notifyNewRequests()
        }
    }

    /// 新建（id 为 nil）或修改通道组：hosts × ports 展开为新成员，tunnelIds 为加入的已有通道
    func saveGroup(id: String?, name: String, owner: String, hosts: String, ports: String, tunnelIds: [String],
                   whitelist: [String], idleCloseMinutes: Int, durationMinutes: Int, maxBandwidthMB: Double) async -> (String?, String?) {
        let body: [String: Any] = [
            "name": name, "owner": owner, "hosts": hosts, "ports": ports, "tunnel_ids": tunnelIds,
            "whitelist": whitelist, "idle_close_minutes": idleCloseMinutes,
            "duration_minutes": durationMinutes, "max_bandwidth_mb": maxBandwidthMB
        ]
        let (data, err) = await apiCall(id == nil ? "POST" : "PUT", id == nil ? "/api/groups" : "/api/groups/\(id!)", body: body)
        guard let data = data else { return (nil, err) }
        let gid = ((try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["group"] as? [String: Any])?["id"] as? String
        await refreshData()
        return (gid, nil)
    }

    /// 预览批量展开结果（不保存）
    func previewGroup(name: String, hosts: String, ports: String) async -> ([GroupPreviewMember], String?) {
        if isPreview {
            // 预览模式只模拟最常见的写法：逗号分隔，以及末段范围 "10.0.3.11-13"
            let hostList = hosts.split(whereSeparator: { ", ，\n".contains($0) }).flatMap { item -> [String] in
                let parts = item.split(separator: ".")
                guard parts.count == 4, let dash = parts[3].firstIndex(of: "-"),
                      let lo = Int(parts[3][..<dash]), let hi = Int(parts[3][parts[3].index(after: dash)...]), lo <= hi else { return [String(item)] }
                return (lo...hi).map { parts[0..<3].joined(separator: ".") + ".\($0)" }
            }
            let portList = ports.split(whereSeparator: { ", ，".contains($0) }).map(String.init)
            var out: [GroupPreviewMember] = []
            for h in hostList { for p in portList {
                out.append(GroupPreviewMember(name: "\(name) (\(h))", target: "\(h):\(p)", listen_port: 33670 + out.count))
            } }
            return (out, nil)
        }
        let (data, err) = await apiCall("POST", "/api/groups/preview", body: ["name": name, "hosts": hosts, "ports": ports])
        guard let data = data else { return ([], err) }
        return ((try? JSONDecoder().decode(GroupPreviewResp.self, from: data))?.members ?? [], nil)
    }

    func toggleGroup(id: String, enabled: Bool) async {
        _ = await apiCall("POST", "/api/groups/\(id)/toggle", body: ["enabled": enabled])
        await refreshData()
    }

    /// 解散通道组；deleteMembers 为 true 时连同组内通道一起删除
    func deleteGroup(id: String, deleteMembers: Bool) async {
        _ = await apiCall("DELETE", "/api/groups/\(id)" + (deleteMembers ? "?delete_members=1" : ""))
        await refreshData()
    }

    /// 把通道移入（groupId 非空）或移出通道组
    func moveTunnel(id: String, toGroup groupId: String) async -> String? {
        let (_, err) = await apiCall("PUT", "/api/tunnels/\(id)", body: ["group_id": groupId])
        await refreshData()
        return err
    }

    /// enabled 为 nil 时取反；传入目标状态时幂等（避免重复点击来回翻转）
    func toggleTunnel(id: String, enabled: Bool? = nil) async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/tunnels/\(id)/toggle") else { return }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        if let enabled = enabled {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try? JSONSerialization.data(withJSONObject: ["enabled": enabled])
        }
        _ = try? await URLSession.shared.data(for: req)
        await refreshData()
    }

    func deleteTunnel(id: String) async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/tunnels/\(id)") else { return }
        var req = URLRequest(url: url)
        req.httpMethod = "DELETE"
        _ = try? await URLSession.shared.data(for: req)
        await refreshData()
    }

    func saveTunnel(
        id: String?,
        name: String,
        target: String,
        port: Int,
        whitelist: [String],
        owner: String,
        durationMinutes: Int,
        idleCloseMinutes: Int,
        maxBandwidthMB: Double,
        groupId: String = ""
    ) async -> (Bool, String?, String?) {
        let isEdit = (id != nil && !id!.isEmpty)
        let endpoint = isEdit ? "http://127.0.0.1:33080/api/tunnels/\(id!)" : "http://127.0.0.1:33080/api/tunnels"
        guard let url = URL(string: endpoint) else { return (false, "无效地址", nil) }

        var req = URLRequest(url: url)
        req.httpMethod = isEdit ? "PUT" : "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")

        let body: [String: Any] = [
            "name": name,
            "target": target,
            "listen_port": port,
            // 空数组表示“仅授权链接”
            "whitelist": whitelist,
            "owner": owner,
            "duration_minutes": durationMinutes,
            "idle_close_minutes": idleCloseMinutes,
            "max_bandwidth_mb": maxBandwidthMB,
            "group_id": groupId
        ]

        guard let httpBody = try? JSONSerialization.data(withJSONObject: body) else {
            return (false, "JSON 序列化失败", nil)
        }
        req.httpBody = httpBody

        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            if let httpResp = resp as? HTTPURLResponse, httpResp.statusCode >= 400 {
                if let errJson = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                   let errMsg = errJson["error"] as? String {
                    return (false, errMsg, nil)
                }
                return (false, "保存失败", nil)
            }
            let savedId = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["id"] as? String
            await refreshData()
            return (true, nil, savedId)
        } catch {
            return (false, error.localizedDescription, nil)
        }
    }

    func probeTarget(target: String) async -> (Bool, String) {
        guard let url = URL(string: "http://127.0.0.1:33080/api/probe") else { return (false, "无效地址") }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let body = ["target": target]
        req.httpBody = try? JSONSerialization.data(withJSONObject: body)

        do {
            let (data, _) = try await URLSession.shared.data(for: req)
            if let res = try? JSONDecoder().decode(ProbeResp.self, from: data) {
                if res.reachable {
                    return (true, "连接成功 (\(res.rtt))")
                } else {
                    return (false, "连接失败: \(res.error ?? "超时")")
                }
            }
            return (false, "解析失败")
        } catch {
            return (false, error.localizedDescription)
        }
    }

    func recommendPort() async -> Int {
        if isPreview { return 33662 }
        guard let url = URL(string: "http://127.0.0.1:33080/api/recommend-port") else { return 33660 }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            if let res = try? JSONDecoder().decode(RecommendPortResp.self, from: data) {
                return res.recommended_port
            }
        } catch {}
        return 33660
    }

    func fetchHistory(scope: AccessScope) async -> [ClientHistoryRecord] {
        if isPreview {
            let ids = Set(scopeTunnels(scope).map { $0.id })
            return previewHistory.filter { ids.contains($0.tunnel_id) }
        }
        guard let url = URL(string: "http://127.0.0.1:33080/api/history?\(scope.historyQuery)") else { return [] }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            return (try? JSONDecoder().decode([ClientHistoryRecord].self, from: data)) ?? []
        } catch {}
        return []
    }

    /// 在线连接与安全事件（之前从未拉取，导致“当前在线客户端”始终为空）
    func fetchConnections() async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/connections") else { return }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            let resp = try JSONDecoder().decode(ConnectionsResp.self, from: data)
            self.activeConns = resp.active_conns
            self.securityEvents = resp.security_events
        } catch {
            self.activeConns = []
        }
    }

    private func apiCall(_ method: String, _ path: String, body: [String: Any]? = nil) async -> (Data?, String?) {
        guard let url = URL(string: "http://127.0.0.1:33080" + path) else { return (nil, "无效地址") }
        var req = URLRequest(url: url)
        req.httpMethod = method
        if let body = body {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try? JSONSerialization.data(withJSONObject: body)
        }
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            if let httpResp = resp as? HTTPURLResponse, httpResp.statusCode >= 400 {
                let errJson = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
                return (nil, (errJson?["error"] as? String) ?? "操作失败 (HTTP \(httpResp.statusCode))")
            }
            return (data, nil)
        } catch {
            return (nil, error.localizedDescription)
        }
    }

    /// 生成邀请链接（validMinutes / maxUses 为 0 时 maxUses 表示不限设备）
    func createInvite(scope: AccessScope, note: String, validMinutes: Int, maxUses: Int, leaseDuration: String, requireApproval: Bool) async -> (InviteShareResp?, String?) {
        if isPreview {
            let link = inviteBase.isEmpty ? "http://192.168.1.10:33000/i/k7m2xq9fpa" : inviteBase + "k7m2xq9fpa"
            let ts = scopeTunnels(scope)
            let host = publicHost.isEmpty ? "192.168.1.10" : publicHost
            let deadline = Date().addingTimeInterval(Double(validMinutes) * 60)
            let fmt = DateFormatter(); fmt.dateFormat = "MM-dd HH:mm"
            let what = scope.isGroup
                ? "通道组：\(scopeTitle(scope))（\(ts.count) 个地址）\n访问地址：" + ts.map { "\n  \(host):\($0.listen_port) → \($0.target)" }.joined()
                : "通道：\(ts.first?.name ?? "MySQL 数据库")\n访问地址：\(host):\(ts.first?.listen_port ?? 33660)"
            let text = "【PortRelay 访问邀请】" + (note.isEmpty ? "" : "（给 \(note)）") + "\n" + what + "\n请在 \(fmt.string(from: deadline)) 前用浏览器打开下面的链接，" + (requireApproval ? "填写姓名提交申请，管理员批准后即可访问" : "填写姓名后即可访问") + "（访问有效期：\(leaseDurationTitle(leaseDuration))）：\n\(link)\n此链接仅限 1 台设备使用，请勿转发。"
            return (InviteShareResp(invite: InviteItem(code: "k7m2xq9fpa", note: note, created_at: "", expires_at: "", max_uses: maxUses, lease_duration: leaseDuration, require_approval: requireApproval, uses: nil), link: link, share_text: text), nil)
        }
        let (data, err) = await apiCall("POST", "\(scope.apiPath)/invites",
                                        body: ["note": note, "valid_minutes": validMinutes, "max_uses": maxUses,
                                               "lease_duration": leaseDuration, "require_approval": requireApproval])
        guard let data = data else { return (nil, err) }
        let resp = try? JSONDecoder().decode(InviteShareResp.self, from: data)
        await refreshData()
        return (resp, resp == nil ? "解析失败" : nil)
    }

    func inviteShare(scope: AccessScope, code: String) async -> (InviteShareResp?, String?) {
        let (data, err) = await apiCall("GET", "\(scope.apiPath)/invites/\(code)/share")
        guard let data = data else { return (nil, err) }
        return (try? JSONDecoder().decode(InviteShareResp.self, from: data), nil)
    }

    func revokeInvite(scope: AccessScope, code: String) async -> String? {
        let (_, err) = await apiCall("DELETE", "\(scope.apiPath)/invites/\(code)")
        try? await Task.sleep(nanoseconds: 1_200_000_000)
        await refreshData()
        return err
    }

    func decideRequest(scope: AccessScope, requestId: String, approve: Bool) async -> String? {
        let (_, err) = await apiCall("POST", "\(scope.apiPath)/requests/\(requestId)", body: ["approve": approve])
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: [requestId])
        try? await Task.sleep(nanoseconds: 1_200_000_000)
        await refreshData()
        return err
    }

    /// 撤销临时放通；ip 为 nil 时撤销该通道（或通道组）全部临时放通。返回错误信息，成功为 nil
    func revokeLease(scope: AccessScope, ip: String?) async -> String? {
        guard let url = URL(string: "http://127.0.0.1:33080\(scope.apiPath)/leases/revoke") else { return "无效地址" }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["ip": ip ?? ""])
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            if let httpResp = resp as? HTTPURLResponse, httpResp.statusCode >= 400 {
                let errJson = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
                return (errJson?["error"] as? String) ?? "撤销失败"
            }
        } catch {
            return error.localizedDescription
        }
        // 服务端下一次指标推送（约 1 秒）后列表才会更新
        try? await Task.sleep(nanoseconds: 1_200_000_000)
        await refreshData()
        return nil
    }

    func killClients(connIds: [String]) async {
        for id in connIds {
            guard let url = URL(string: "http://127.0.0.1:33080/api/connections/kill") else { return }
            var req = URLRequest(url: url)
            req.httpMethod = "POST"
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try? JSONSerialization.data(withJSONObject: ["conn_id": id])
            _ = try? await URLSession.shared.data(for: req)
        }
        await refreshData()
    }

    func killClient(connId: String) async {
        guard let url = URL(string: "http://127.0.0.1:33080/api/connections/kill") else { return }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let body = ["conn_id": connId]
        req.httpBody = try? JSONSerialization.data(withJSONObject: body)
        _ = try? await URLSession.shared.data(for: req)
        await refreshData()
    }

    func fetchSettings() async -> (String, String, String) {
        if isPreview { return ("192.168.1.10:33000", "preview-token", "192.168.1.10") }
        guard let url = URL(string: "http://127.0.0.1:33080/api/settings") else { return ("", "", "") }
        do {
            let (data, _) = try await URLSession.shared.data(from: url)
            if let s = try? JSONDecoder().decode(SettingsResp.self, from: data) {
                return (s.server_addr, s.token, s.public_host ?? self.publicHost)
            }
        } catch {}
        return (self.serverAddr, "", self.publicHost)
    }

    /// 返回错误信息，成功时为 nil（后端保存后会自动重连）
    func saveSettings(serverAddr: String, token: String, publicHost: String) async -> String? {
        guard let url = URL(string: "http://127.0.0.1:33080/api/settings") else { return "无效地址" }
        var req = URLRequest(url: url)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let body = ["server_addr": serverAddr, "token": token, "public_host": publicHost]
        req.httpBody = try? JSONSerialization.data(withJSONObject: body)
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            if let httpResp = resp as? HTTPURLResponse, httpResp.statusCode >= 400 {
                let errJson = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
                return (errJson?["error"] as? String) ?? "保存失败"
            }
        } catch {
            return error.localizedDescription
        }
        await refreshData()
        runDiagnostics()
        return nil
    }

    // Live Diagnostics Check
    /// 粗略判断服务器地址所在的网络类型，仅用于诊断展示
    var serverNetworkKind: String {
        let host = hostOf(serverAddr)
        let parts = host.split(separator: ".").compactMap { Int($0) }
        guard parts.count == 4 else { return host.isEmpty ? "未配置" : "域名" }
        switch (parts[0], parts[1]) {
        case (127, _): return "本机"
        case (10, _), (192, 168), (172, 16...31): return "私有网络"
        case (100, 64...127): return "虚拟组网"
        default: return "公网"
        }
    }

    func runDiagnostics() {
        self.isRunningDiagnostics = true
        var items: [DiagnosticItem] = []

        if self.isConnected {
            items.append(DiagnosticItem(
                title: "本地网络访问权限 (Local Network)",
                desc: "macOS 隐私与安全性授权状态正常，可与本地及内网节点通信。",
                status: .ok,
                statusText: "已授权",
                actionTitle: nil,
                action: nil
            ))
        } else {
            items.append(DiagnosticItem(
                title: "本地网络访问权限 (Local Network)",
                desc: "若曾误点击拒绝，系统会阻止本应用访问局域网内的网关或目标服务。",
                status: .warning,
                statusText: "需确认授权",
                actionTitle: "打开系统设置 > 本地网络",
                action: {
                    if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_LocalNetwork") {
                        NSWorkspace.shared.open(url)
                    }
                }
            ))
        }

        if self.isConnected {
            items.append(DiagnosticItem(
                title: "中继服务器通信",
                desc: "已与 \(self.serverAddr) 建立长连接，端口池处于就绪状态。",
                status: .ok,
                statusText: "连通正常",
                actionTitle: nil,
                action: nil
            ))
        } else {
            items.append(DiagnosticItem(
                title: "中继服务器通信",
                desc: "无法与 \(self.serverAddr) 握手。" + (self.lastError.isEmpty ? "请确保中继服务器上的服务正在运行且防火墙已放通控制端口。" : "原因：\(self.lastError)"),
                status: .error,
                statusText: "无法连接",
                actionTitle: "重试连接",
                action: {
                    Task { @MainActor in
                        await self.refreshData()
                        self.runDiagnostics()
                    }
                }
            ))
        }

        items.append(DiagnosticItem(
            title: "网络路径",
            desc: "经\(serverNetworkKind)连接边缘网关 \(self.serverAddr)。连接方式不限：局域网、VPN、Tailscale / ZeroTier 等组网或公网均可，只要本机同时能访问目标网段即可。",
            status: .ok,
            statusText: serverNetworkKind,
            actionTitle: nil,
            action: nil
        ))

        items.append(DiagnosticItem(
            title: "后台中继引擎 (PortRelay Core)",
            desc: self.agentReachable ? "负责与服务器保持连接，并把访问流量转发到内网目标服务。" : "本地 127.0.0.1:33080 无响应，正在尝试自动拉起后台引擎。",
            status: self.agentReachable ? .ok : .error,
            statusText: self.agentReachable ? "运行中" : "未运行",
            actionTitle: nil,
            action: nil
        ))

        self.diagnostics = items
        self.isRunningDiagnostics = false
    }
}

// MARK: - Native Views

@MainActor
struct ContentView: View {
    @ObservedObject var vm: PortRelayViewModel
    @State private var showingAddSheet = false
    @State private var showingSettingsSheet = false
    @State private var showingDiagnosticsSheet = false
    // 用 sheet(item:) 直接把要操作的通道传给弹窗；
    // 之前 isPresented + selectedTunnel 的写法会读到旧值，导致编辑弹窗偶尔为空或显示别的通道
    @State private var editingTunnel: TunnelItem?
    /// 邀请弹窗：通道 + 是否打开即生成（新建通道后直接出分享卡片）
    @State private var inviteTarget: InviteSheetTarget?
    @State private var clientsTarget: ClientsSheetTarget?
    /// 新建（group 为 nil）或编辑通道组
    @State private var groupSheet: GroupSheetTarget?
    // 通道搜索与状态筛选
    @State private var searchText: String = ""
    @State private var statusFilter: TunnelStatusFilter = .all
    @FocusState private var searchFocused: Bool

    init() {
        self.init(vm: PortRelayViewModel.shared)
    }

    init(vm: PortRelayViewModel, initialSearch: String = "", initialFilter: TunnelStatusFilter = .all) {
        _vm = ObservedObject(wrappedValue: vm)
        _searchText = State(initialValue: initialSearch)
        _statusFilter = State(initialValue: initialFilter)
    }

    /// 卡片列表：独立通道与通道组按配置顺序混排（组出现在它第一个成员的位置）
    enum CardEntry: Identifiable {
        case tunnel(TunnelItem)
        case group(GroupItem, [TunnelItem])

        var id: String {
            switch self {
            case .tunnel(let t): return "t:" + t.id
            case .group(let g, _): return "g:" + g.id
            }
        }
    }

    var entries: [CardEntry] {
        var out: [CardEntry] = []
        var seen = Set<String>()
        let query = searchText
        for t in vm.tunnels {
            if let g = vm.group(t.group_id) {
                guard !seen.contains(g.id) else { continue }
                seen.insert(g.id)
                // 组名或责任人命中时显示整组成员，否则只显示命中的成员
                let groupHit = !query.trimmingCharacters(in: .whitespaces).isEmpty && TunnelSearch.matches(texts: [g.name, g.owner ?? ""], query: query)
                let shown = vm.members(of: g).filter {
                    statusFilter.accepts($0) && (groupHit || TunnelSearch.matches($0, query: query, publicHost: vm.publicHost))
                }
                if !shown.isEmpty { out.append(.group(g, shown)) }
            } else if statusFilter.accepts(t) && TunnelSearch.matches(t, query: query, publicHost: vm.publicHost) {
                out.append(.tunnel(t))
            }
        }
        // 还没有成员的组
        for g in vm.groups where !seen.contains(g.id) && g.tunnel_ids.isEmpty {
            if statusFilter == .all && TunnelSearch.matches(texts: [g.name, g.owner ?? ""], query: query) {
                out.append(.group(g, []))
            }
        }
        return out
    }

    var matchedTunnelCount: Int {
        entries.reduce(0) { n, e in
            switch e {
            case .tunnel: return n + 1
            case .group(_, let shown): return n + shown.count
            }
        }
    }

    var isFiltering: Bool { !searchText.trimmingCharacters(in: .whitespaces).isEmpty || statusFilter != .all }

    var body: some View {
        VStack(spacing: 0) {
            // macOS Standard Top Bar
            topBarView
                .padding(.horizontal, 18)
                .padding(.vertical, 12)
                .background(Color(NSColor.windowBackgroundColor))
            
            Divider()

            if !vm.isConnected {
                HStack(spacing: 10) {
                    Image(systemName: "exclamationmark.circle.fill")
                        .foregroundColor(.orange)
                        .font(.system(size: 13))
                    Text("未连接至中继服务器 (\(vm.serverAddr))" + (vm.lastError.isEmpty ? "" : " · \(vm.lastError)"))
                        .font(.system(size: 12))
                        .foregroundColor(.primary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    Spacer()
                    Button("查看授权与诊断") {
                        showingDiagnosticsSheet = true
                    }
                    .buttonStyle(.link)
                    .font(.system(size: 12))
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 7)
                .background(Color.orange.opacity(0.08))
                Divider()
            }

            ScrollView {
                VStack(spacing: 16) {
                    // Minimalist Metrics Strip (Activity Monitor style)
                    metricsStripView

                    // Tunnel Section Header: 标题 / 数量 ……… 状态筛选 / 搜索
                    HStack(spacing: 12) {
                        Text("中继通道")
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundColor(.secondary)
                        Text(verbatim: isFiltering ? "匹配 \(matchedTunnelCount) / 共 \(vm.tunnels.count) 个"
                             : "\(vm.tunnels.count) 个通道" + (vm.groups.isEmpty ? "" : " · \(vm.groups.count) 个组"))
                            .font(.system(size: 12))
                            .foregroundColor(.secondary)
                            .monospacedDigit()
                        Spacer()
                        if !vm.tunnels.isEmpty {
                            Picker("", selection: $statusFilter) {
                                ForEach(TunnelStatusFilter.allCases, id: \.self) { f in
                                    Text(f.rawValue).tag(f)
                                }
                            }
                            .pickerStyle(.segmented)
                            .labelsHidden()
                            .fixedSize()
                            .controlSize(.small)

                            searchField
                        }
                    }
                    .padding(.top, 4)

                    // Tunnel Cards
                    if vm.tunnels.isEmpty && vm.groups.isEmpty {
                        emptyStateView
                    } else if entries.isEmpty {
                        noMatchView
                    } else {
                        VStack(spacing: 10) {
                            ForEach(entries) { entry in
                                switch entry {
                                case .group(let g, let shown):
                                    GroupCardView(
                                        group: g,
                                        members: vm.members(of: g),
                                        visible: shown,
                                        vm: vm,
                                        onEdit: { groupSheet = GroupSheetTarget(group: g) },
                                        onInvite: { inviteTarget = InviteSheetTarget(scope: .group(g.id), autoGenerate: false) },
                                        onViewClients: { tab in clientsTarget = ClientsSheetTarget(scope: .group(g.id), tab: tab) },
                                        onEditMember: { t in editingTunnel = t },
                                        onInviteMember: { t in inviteTarget = InviteSheetTarget(tunnel: t, autoGenerate: false) },
                                        onMemberClients: { t in clientsTarget = ClientsSheetTarget(tunnel: t, tab: 0) }
                                    )
                                case .tunnel(let t):
                                TunnelCardView(
                                    tunnel: t,
                                    vm: vm,
                                    onEdit: {
                                        editingTunnel = t
                                    },
                                    onViewClients: {
                                        clientsTarget = ClientsSheetTarget(tunnel: t, tab: 0)
                                    },
                                    onViewLeases: {
                                        clientsTarget = ClientsSheetTarget(tunnel: t, tab: 2)
                                    },
                                    onInvite: {
                                        inviteTarget = InviteSheetTarget(tunnel: t, autoGenerate: false)
                                    }
                                )
                                }
                            }
                        }
                    }
                }
                .padding(18)
            }
        }
        .frame(minWidth: 920, minHeight: 640)
        .sheet(isPresented: $showingAddSheet) {
            TunnelEditSheet(vm: vm, tunnel: nil, isPresented: $showingAddSheet, onCreated: { id in
                // 新建完成后打开邀请设置（时长、审批在邀请中选择）
                Task { @MainActor in
                    try? await Task.sleep(nanoseconds: 600_000_000)
                    if let t = vm.tunnels.first(where: { $0.id == id }) {
                        inviteTarget = InviteSheetTarget(tunnel: t, autoGenerate: false)
                    }
                }
            })
        }
        .sheet(item: $inviteTarget) { target in
            InviteSheet(vm: vm, scope: target.scope, autoGenerate: target.autoGenerate, isPresented: Binding(
                get: { inviteTarget != nil },
                set: { if !$0 { inviteTarget = nil } }
            ))
        }
        .sheet(item: $editingTunnel) { t in
            TunnelEditSheet(vm: vm, tunnel: t, isPresented: Binding(
                get: { editingTunnel != nil },
                set: { if !$0 { editingTunnel = nil } }
            ))
        }
        .sheet(item: $groupSheet) { target in
            GroupEditSheet(vm: vm, group: target.group, isPresented: Binding(
                get: { groupSheet != nil },
                set: { if !$0 { groupSheet = nil } }
            ), onCreated: { gid in
                // 新建完成后直接打开整组的邀请设置
                Task { @MainActor in
                    try? await Task.sleep(nanoseconds: 600_000_000)
                    if vm.scopeEnabled(.group(gid)) {
                        inviteTarget = InviteSheetTarget(scope: .group(gid), autoGenerate: false)
                    }
                }
            })
        }
        .sheet(isPresented: $showingSettingsSheet) {
            SettingsSheet(vm: vm, isPresented: $showingSettingsSheet)
        }
        .sheet(isPresented: $showingDiagnosticsSheet) {
            DiagnosticsSheet(vm: vm, isPresented: $showingDiagnosticsSheet)
        }
        .sheet(item: $clientsTarget) { target in
            ClientDetailsSheet(vm: vm, scope: target.scope, initialTab: target.tab, isPresented: Binding(
                get: { clientsTarget != nil },
                set: { if !$0 { clientsTarget = nil } }
            ))
        }
    }

    var topBarView: some View {
        HStack(spacing: 14) {
            HStack(spacing: 8) {
                Image(systemName: "arrow.left.arrow.right.square.fill")
                    .font(.system(size: 20))
                    .foregroundColor(.accentColor)
                VStack(alignment: .leading, spacing: 1) {
                    Text("PortRelay")
                        .font(.system(size: 14, weight: .bold))
                    Text("端口中继控制台")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                }
            }

            Spacer()

            HStack(spacing: 6) {
                Circle()
                    .fill(vm.isConnected ? Color.green : Color.orange)
                    .frame(width: 7, height: 7)
                Text(vm.isConnected ? "已就绪" : "连接中断")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .background(Color(NSColor.controlBackgroundColor))
            .cornerRadius(6)

            Button(action: {
                vm.runDiagnostics()
                showingDiagnosticsSheet = true
            }) {
                Label("诊断与权限", systemImage: "shield")
            }
            .buttonStyle(.bordered)
            .controlSize(.small)

            Button(action: { groupSheet = GroupSheetTarget(group: nil) }) {
                Label("新建通道组", systemImage: "square.stack.3d.up")
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .help("一次添加多个主机 / 端口，共用一条邀请链接")

            Button(action: { showingAddSheet = true }) {
                Label("添加通道", systemImage: "plus")
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.small)

            Button(action: { showingSettingsSheet = true }) {
                Image(systemName: "gearshape")
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
        }
    }

    var metricsStripView: some View {
        HStack(spacing: 1) {
            MetricStripItem(
                title: "活跃通道",
                value: "\(vm.tunnels.filter { $0.enabled }.count) / \(vm.tunnels.count)",
                caption: "端口池 33001-34000"
            )
            Divider().frame(height: 36)
            HStack(alignment: .center, spacing: 10) {
                MetricStripItem(
                    title: "实时速率",
                    value: "↑ \(formatRate(vm.totalRateIn))  ↓ \(formatRate(vm.totalRateOut))",
                    caption: "上行 / 下行 · 近 10 分钟"
                )
                .fixedSize()
                RateSparkline(series: vm.totalRateHistory, height: 34)
                    .frame(minWidth: 60, maxWidth: 140)
                    .padding(.trailing, 10)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Divider().frame(height: 36)
            MetricStripItem(
                title: "活跃连接",
                value: "\(vm.totalActiveConns)",
                caption: "历史累计 \(vm.totalHistoricalConns)"
            )
            Divider().frame(height: 36)
            MetricStripItem(
                title: "拦截未授权",
                value: "\(vm.totalBlockedAttempts)",
                caption: "白名单保护",
                highlight: vm.totalBlockedAttempts > 0
            )
        }
        .padding(.vertical, 10)
        .padding(.horizontal, 12)
        .background(RoundedRectangle(cornerRadius: 10).fill(Color(NSColor.controlBackgroundColor)))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.primary.opacity(0.09), lineWidth: 1))
    }

    var searchField: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 11))
                .foregroundColor(.secondary)
            TextField("搜索名称、地址、端口、责任人…", text: $searchText)
                .textFieldStyle(.plain)
                .font(.system(size: 12))
                .focused($searchFocused)
                .onExitCommand { searchText = "" }
            if !searchText.isEmpty {
                Button(action: { searchText = "" }) {
                    Image(systemName: "xmark.circle.fill")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                }
                .buttonStyle(.plain)
                .help("清除搜索 (Esc)")
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 5)
        .frame(width: 250)
        .background(RoundedRectangle(cornerRadius: 7).fill(Color(NSColor.controlBackgroundColor)))
        .overlay(RoundedRectangle(cornerRadius: 7).strokeBorder(searchFocused ? Color.accentColor.opacity(0.7) : Color.primary.opacity(0.12), lineWidth: 1))
        .help("支持多个关键字（空格分隔），中文可用拼音或首字母，例如 sjk。快捷键 ⌘F")
        .background(
            // ⌘F 聚焦搜索框
            Button("") { searchFocused = true }
                .keyboardShortcut("f", modifiers: .command)
                .opacity(0)
        )
    }

    var noMatchView: some View {
        VStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 26))
                .foregroundColor(.secondary)
            Text("没有匹配的通道")
                .font(.system(size: 13, weight: .medium))
            Text(verbatim: searchText.isEmpty ? "当前筛选：\(statusFilter.rawValue)" : "关键字：\(searchText)" + (statusFilter == .all ? "" : " · 筛选：\(statusFilter.rawValue)"))
                .font(.system(size: 12))
                .foregroundColor(.secondary)
            Button("清除搜索与筛选") {
                searchText = ""
                statusFilter = .all
            }
            .controlSize(.small)
            .padding(.top, 2)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 40)
        .rowCard()
    }

    var emptyStateView: some View {
        VStack(spacing: 8) {
            Image(systemName: "tray")
                .font(.system(size: 32))
                .foregroundColor(.secondary)
            Text("无活动通道")
                .font(.system(size: 13, weight: .medium))
            Text("点击右上角「添加通道」创建反向中继端口；需要一次开放多台主机时，用「新建通道组」")
                .font(.system(size: 12))
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 48)
        .background(Color(NSColor.controlBackgroundColor).opacity(0.4))
        .cornerRadius(8)
    }
}

struct MetricStripItem: View {
    let title: String
    let value: String
    let caption: String
    var highlight: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title)
                .font(.system(size: 11))
                .foregroundColor(.secondary)
            Text(value)
                .font(.system(size: 13, weight: .semibold, design: .monospaced))
                .foregroundColor(highlight ? .red : .primary)
            Text(caption)
                .font(.system(size: 10))
                .foregroundColor(.secondary.opacity(0.8))
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 10)
    }
}

// MARK: - Tunnel Card View (With TTL, Owner, Session/History Traffic)

struct TunnelCardView: View {
    let tunnel: TunnelItem
    @ObservedObject var vm: PortRelayViewModel
    let onEdit: () -> Void
    let onViewClients: () -> Void
    let onViewLeases: () -> Void
    let onInvite: () -> Void

    @State private var showDeleteConfirm: Bool = false

    var isOnline: Bool {
        tunnel.enabled && (tunnel.metrics?.status == "online")
    }

    var statusBadge: (text: String, color: Color) {
        guard tunnel.enabled else { return ("已暂停", .secondary) }
        switch tunnel.metrics?.status {
        case "online": return ("运行中", .green)
        case "error": return ("异常", .red)
        case "offline": return ("等待连接服务器", .orange)
        default: return ("同步中", .secondary)
        }
    }

    @State private var cardWidth: CGFloat = 0

    /// 卡片足够宽时在右侧显示流量面板
    var showsTraffic: Bool { cardWidth == 0 || cardWidth >= 700 }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            // 主体左右两栏：左侧 名称/地址/时间口径，右侧 速率 + 开关 + 流量曲线（与左侧等高）
            HStack(alignment: .top, spacing: 20) {
                VStack(alignment: .leading, spacing: 9) {
                    HStack(alignment: .center, spacing: 8) {
                        Text(verbatim: tunnel.name)
                            .font(.system(size: 14, weight: .semibold))
                            .lineLimit(1)
                            .truncationMode(.tail)
                            .layoutPriority(1)

                        if let owner = tunnel.owner, !owner.isEmpty {
                            HStack(spacing: 3) {
                                Image(systemName: "person.fill")
                                    .font(.system(size: 9))
                                Text(verbatim: owner)
                                    .font(.system(size: 11, weight: .medium))
                                    .lineLimit(1)
                            }
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(Color.blue.opacity(0.12))
                            .foregroundColor(.blue)
                            .cornerRadius(4)
                        }

                        Text(verbatim: statusBadge.text)
                            .font(.system(size: 10, weight: .medium))
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1.5)
                            .background(statusBadge.color.opacity(0.12))
                            .foregroundColor(statusBadge.color)
                            .cornerRadius(4)
                            .help(tunnel.metrics?.error ?? "")

                        accessTag
                    }

                    if tunnel.enabled, tunnel.metrics?.status == "error", let err = tunnel.metrics?.error, !err.isEmpty {
                        Label {
                            Text(verbatim: "服务器注册失败：\(err)")
                        } icon: {
                            Image(systemName: "exclamationmark.triangle.fill")
                        }
                        .font(.system(size: 11))
                        .foregroundColor(.red)
                        .lineLimit(2)
                    }

                    routeRow

                    FlowLayout(spacing: 14, lineSpacing: 7) {
                        lifecycleItems
                        badgeItems
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                if tunnel.enabled && showsTraffic {
                    VStack(alignment: .trailing, spacing: 6) {
                        HStack(spacing: 10) {
                            RateLegend(down: tunnel.metrics?.rate_out ?? 0, up: tunnel.metrics?.rate_in ?? 0)
                            Spacer(minLength: 0)
                            enableToggle
                        }
                        InlineTrafficChart(series: tunnel.rate_history)
                    }
                    .frame(width: 250)
                } else {
                    // 无曲线时右栏只放开关，固定尺寸，不挤占左侧
                    enableToggle
                        .fixedSize()
                }
            }

            Divider()

            // Footer: 连接 / 本次 / 历史 ……… 操作
            HStack(spacing: 12) {
                let m = tunnel.metrics
                let sessionBytes = (m?.bytes_in ?? 0) + (m?.bytes_out ?? 0)
                let histBytes = (tunnel.historical_bytes_in ?? 0) + (tunnel.historical_bytes_out ?? 0) + (tunnel.enabled ? sessionBytes : 0)
                HStack(spacing: 16) {
                    footerStat("连接", "\(m?.active_conns ?? 0)")
                    footerStat("本次", formatBytes(sessionBytes))
                    footerStat("历史", formatBytes(histBytes))
                    if !showsTraffic && tunnel.enabled {
                        footerStat("速率", "↑\(formatRate(m?.rate_in ?? 0)) ↓\(formatRate(m?.rate_out ?? 0))")
                    }
                }
                .lineLimit(1)

                Spacer(minLength: 8)

                HStack(spacing: 6) {
                    Button(action: onInvite) {
                        Label("邀请", systemImage: "square.and.arrow.up")
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(!tunnel.enabled)
                    .help(tunnel.enabled ? "生成邀请链接，发给同事临时访问" : "请先开启通道")

                    Button("连接与授权", action: onViewClients)
                        .buttonStyle(.bordered)
                        .controlSize(.small)

                    Button("编辑", action: onEdit)
                        .buttonStyle(.bordered)
                        .controlSize(.small)

                    Button("删除", role: .destructive) {
                        showDeleteConfirm = true
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.small)
                }
            }
        }
        .padding(14)
        .background(GeometryReader { g in Color.clear.preference(key: CardWidthKey.self, value: g.size.width) })
        .onPreferenceChange(CardWidthKey.self) { cardWidth = $0 }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color(NSColor.controlBackgroundColor)))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.primary.opacity(0.09), lineWidth: 1))
        .shadow(color: .black.opacity(0.04), radius: 2, y: 1)
        .opacity(tunnel.enabled ? 1.0 : 0.6)
        .alert("确认删除通道？", isPresented: $showDeleteConfirm) {
            Button("取消", role: .cancel) {}
            Button("确认删除", role: .destructive) {
                Task { await vm.deleteTunnel(id: tunnel.id) }
            }
        } message: {
            Text(verbatim: "确定要删除通道「\(tunnel.name)」吗？\n\n• 对外访问地址：\(vm.publicHost):\(tunnel.listen_port)\n• 目标服务：\(tunnel.target)\n\n删除后将立即注销中转监听并切断所有活跃连接。此操作不可撤销。")
        }
    }

    // MARK: Card parts

    var enableToggle: some View {
        Toggle("", isOn: Binding(
            get: { tunnel.enabled },
            set: { newValue in
                Task { await vm.toggleTunnel(id: tunnel.id, enabled: newValue) }
            }
        ))
        .toggleStyle(.switch)
        .controlSize(.mini)
        .labelsHidden()
        .help(tunnel.enabled ? "关闭通道" : "开启通道")
    }

    var routeRow: some View {
        HStack(spacing: 8) {
            Text(verbatim: "\(vm.publicHost):\(tunnel.listen_port)")
                .font(.system(size: 12, weight: .medium, design: .monospaced))
                .foregroundColor(.accentColor)
            Image(systemName: "arrow.right")
                .font(.system(size: 10))
                .foregroundColor(.secondary)
            Text(verbatim: tunnel.target)
                .font(.system(size: 12, weight: .medium, design: .monospaced))
                .foregroundColor(.secondary)
            Menu {
                ForEach(quickConnectSnippets(), id: \.cmd) { item in
                    Button(action: { copyToPasteboard(item.cmd) }) {
                        Label(item.title, systemImage: item.icon)
                    }
                }
            } label: {
                HStack(spacing: 3) {
                    Image(systemName: "doc.on.doc")
                        .font(.system(size: 10))
                    Text("一键复制")
                        .font(.system(size: 11))
                }
                .foregroundColor(.secondary)
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            .help("一键复制客户端连接命令或访问地址")
        }
        .lineLimit(1)
    }

    var accessTag: some View {
        Group {
            if tunnel.whitelist.isEmpty {
                capsule("lock.fill", "仅授权链接", .green)
            } else if tunnel.whitelist.contains("*") {
                capsule("exclamationmark.triangle.fill", "全部放通 *", .red)
                    .help("任何能访问服务器的设备都可以连接，建议改为“仅授权链接”")
            } else {
                capsule("network", tunnel.whitelist.count == 1 ? "\(tunnel.whitelist[0]) + 授权链接" : "\(tunnel.whitelist.count) 个 IP/网段 + 授权链接", .secondary)
                    .help("固定白名单：" + tunnel.whitelist.joined(separator: ", "))
            }
        }
    }

    func capsule(_ icon: String, _ text: String, _ color: Color) -> some View {
        cardCapsule(icon, text, color)
    }

    @ViewBuilder
    var lifecycleItems: some View {
        if tunnel.enabled {
            lifecycleItem("play.circle", "开启 \(formatDateTimeShort(tunnel.started_at))")
            if let first = tunnel.first_connected_at {
                lifecycleItem("person.wave.2", "首次连接 \(formatDateTimeShort(first))")
            } else {
                lifecycleItem("person.wave.2", "尚无连接")
            }
            lifecycleItem("stopwatch", "实际连接 \(formatSecondsToDuration(tunnel.activity?.connected_sec ?? tunnel.connected_sec ?? 0))",
                          color: .primary, weight: .medium)
                .help("本次开启后，至少有一个连接在线的累计时长")
            if let act = tunnel.activity {
                if act.in_use {
                    lifecycleItem("circle.fill", "使用中", color: .green, weight: .medium, iconSize: 7)
                        .help("有在线连接、有效授权、待审批申请或可用的邀请链接")
                } else if let closeAt = act.auto_close_at {
                    let left = max(0, -secondsSince(closeAt))
                    lifecycleItem("moon.zzz", "已闲置 \(formatSecondsToDuration(secondsSince(act.idle_since)))，\(formatSecondsToDuration(left)) 后自动关闭",
                                  color: left < 300 ? .orange : .secondary)
                } else {
                    lifecycleItem("moon.zzz", "已闲置 \(formatSecondsToDuration(secondsSince(act.idle_since)))（不自动关闭）")
                }
            }
            if tunnel.expires_at != nil {
                let remaining = formatRemainingTime(expiresAt: tunnel.expires_at)
                lifecycleItem("hourglass", "最长开启至 \(formatDateTimeShort(tunnel.expires_at))",
                              color: remaining.isExpiringSoon ? .orange : .secondary)
            }
        } else {
            if let lastSec = tunnel.last_duration_sec, lastSec > 0 {
                lifecycleItem("clock.arrow.circlepath", "上次运行 \(formatSecondsToDuration(lastSec)) · 实际连接 \(formatSecondsToDuration(tunnel.last_connected_sec ?? 0))")
            }
            if let reason = tunnel.close_reason, !reason.isEmpty {
                lifecycleItem("power", "关闭原因：\(reason)")
            }
            let idle = tunnel.effective_idle_close_minutes ?? 30
            lifecycleItem("moon.zzz", "闲置自动关闭：" + (idle > 0 ? idleCloseTitle(idle) : "关"))
            if let d = tunnel.duration_minutes, d > 0 {
                lifecycleItem("hourglass", "最长开启 \(formatSecondsToDuration(Int64(d) * 60))")
            }
        }
    }

    @ViewBuilder
    var badgeItems: some View {
        if let mb = tunnel.max_bandwidth_mb, mb > 0 {
            capsule("gauge.with.needle", "限速 \(Int(mb)) MB/s", .purple)
        }
        if let pending = tunnel.metrics?.pending, !pending.isEmpty {
            Button(action: onViewLeases) {
                capsule("bell.badge.fill", "待审批 \(pending.count)", .orange)
            }
            .buttonStyle(.plain)
            .help("有同事申请访问，点击审批")
        }
        if let leases = tunnel.metrics?.dynamic_leases, !leases.isEmpty {
            Button(action: onViewLeases) {
                HStack(spacing: 2) {
                    capsule("person.crop.circle.badge.checkmark", "已授权 \(leases.count) 台", .green)
                }
            }
            .buttonStyle(.plain)
            .help("查看与撤销已授权的设备")
        }
    }

    func footerStat(_ label: String, _ value: String) -> some View {
        cardFooterStat(label, value)
    }

    func lifecycleItem(_ icon: String, _ text: String, color: Color = .secondary, weight: Font.Weight = .regular, iconSize: CGFloat = 11) -> some View {
        cardLifecycleItem(icon, text, color: color, weight: weight, iconSize: iconSize)
    }

    func quickConnectSnippets() -> [(title: String, cmd: String, icon: String)] {
        connectSnippets(tunnel, host: vm.publicHost)
    }
}

/// 一键复制菜单：按目标端口 / 名称猜测客户端命令，并总是提供访问地址
func connectSnippets(_ tunnel: TunnelItem, host: String) -> [(title: String, cmd: String, icon: String)] {
        let p = tunnel.listen_port
        let targetLower = tunnel.target.lowercased()
        let nameLower = tunnel.name.lowercased()
        var list: [(String, String, String)] = []

        if targetLower.hasSuffix(":22") || nameLower.contains("ssh") {
            list.append(("复制 SSH 命令: ssh -p \(p) root@\(host)", "ssh -p \(p) root@\(host)", "terminal"))
        } else if targetLower.hasSuffix(":3306") || nameLower.contains("mysql") {
            list.append(("复制 MySQL 命令: mysql -h \(host) -P \(p) -u root -p", "mysql -h \(host) -P \(p) -u root -p", "cylinder"))
        } else if targetLower.hasSuffix(":6379") || nameLower.contains("redis") {
            list.append(("复制 Redis 命令: redis-cli -h \(host) -p \(p)", "redis-cli -h \(host) -p \(p)", "server.rack"))
        } else if targetLower.hasSuffix(":80") || targetLower.hasSuffix(":443") || targetLower.hasSuffix(":8080") || nameLower.contains("web") || nameLower.contains("http") {
            list.append(("复制 Web URL: http://\(host):\(p)", "http://\(host):\(p)", "globe"))
        }

        list.append(("复制 访问地址 (\(host):\(p))", "\(host):\(p)", "network"))
        return list
}

// MARK: - Tunnel Group Card

/// 通道组卡片：上半部分是整组的状态与流量，下面逐行列出成员（每个成员一个访问地址）
struct GroupCardView: View {
    let group: GroupItem
    /// 组内全部通道（统计用）
    let members: [TunnelItem]
    /// 当前搜索 / 筛选下显示的成员
    let visible: [TunnelItem]
    @ObservedObject var vm: PortRelayViewModel
    let onEdit: () -> Void
    let onInvite: () -> Void
    let onViewClients: (Int) -> Void
    let onEditMember: (TunnelItem) -> Void
    let onInviteMember: (TunnelItem) -> Void
    let onMemberClients: (TunnelItem) -> Void

    @State private var showDeleteDialog = false
    @State private var memberToDelete: TunnelItem?
    @State private var cardWidth: CGFloat = 0

    var enabledMembers: [TunnelItem] { members.filter { $0.enabled } }
    var anyEnabled: Bool { !enabledMembers.isEmpty }
    var showsTraffic: Bool { cardWidth == 0 || cardWidth >= 700 }
    var rateIn: Int64 { enabledMembers.reduce(0) { $0 + ($1.metrics?.rate_in ?? 0) } }
    var rateOut: Int64 { enabledMembers.reduce(0) { $0 + ($1.metrics?.rate_out ?? 0) } }
    var groupLeases: [DynamicLeaseItem] { group.metrics?.dynamic_leases ?? [] }
    var groupPending: [AccessRequestItem] { group.metrics?.pending ?? [] }

    var statusBadge: (text: String, color: Color) {
        if members.isEmpty { return ("暂无通道", .secondary) }
        let on = enabledMembers.count
        if on == 0 { return ("已暂停", .secondary) }
        if enabledMembers.contains(where: { $0.metrics?.status == "error" }) { return ("部分异常", .red) }
        if enabledMembers.contains(where: { $0.metrics?.status == "offline" }) { return ("等待连接服务器", .orange) }
        if on < members.count { return ("\(on)/\(members.count) 运行中", .orange) }
        return ("运行中", .green)
    }

    var portRange: String? {
        let ports = members.map { $0.listen_port }.sorted()
        guard let lo = ports.first, let hi = ports.last else { return nil }
        let contiguous = hi - lo + 1 == ports.count
        if lo == hi { return "\(lo)" }
        return contiguous ? "\(lo)–\(hi)" : ports.map(String.init).joined(separator: ", ")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top, spacing: 20) {
                VStack(alignment: .leading, spacing: 9) {
                    HStack(alignment: .center, spacing: 8) {
                        Image(systemName: "square.stack.3d.up.fill")
                            .font(.system(size: 12))
                            .foregroundColor(.accentColor)
                        Text(verbatim: group.name)
                            .font(.system(size: 14, weight: .semibold))
                            .lineLimit(1)
                            .truncationMode(.tail)
                            .layoutPriority(1)
                        if let owner = group.owner, !owner.isEmpty {
                            ownerChip(owner)
                        }
                        cardCapsule(nil, statusBadge.text, statusBadge.color)
                        accessTag
                    }

                    HStack(spacing: 8) {
                        Text(verbatim: "\(members.count) 个地址")
                            .font(.system(size: 12, weight: .medium))
                            .foregroundColor(.secondary)
                        if let range = portRange {
                            Text(verbatim: "\(vm.publicHost):\(range)")
                                .font(.system(size: 12, weight: .medium, design: .monospaced))
                                .foregroundColor(.accentColor)
                                .lineLimit(1)
                                .truncationMode(.tail)
                        }
                        if !members.isEmpty {
                            Menu {
                                Button(action: { copyToPasteboard(mappingText) }) {
                                    Label("复制全部访问地址（含目标）", systemImage: "list.bullet")
                                }
                                Button(action: { copyToPasteboard(members.map { "\(vm.publicHost):\($0.listen_port)" }.joined(separator: "\n")) }) {
                                    Label("仅复制访问地址", systemImage: "network")
                                }
                            } label: {
                                HStack(spacing: 3) {
                                    Image(systemName: "doc.on.doc")
                                        .font(.system(size: 10))
                                    Text("一键复制")
                                        .font(.system(size: 11))
                                }
                                .foregroundColor(.secondary)
                            }
                            .menuStyle(.borderlessButton)
                            .fixedSize()
                        }
                    }

                    FlowLayout(spacing: 14, lineSpacing: 7) {
                        lifecycleItems
                        badgeItems
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)

                if anyEnabled && showsTraffic {
                    VStack(alignment: .trailing, spacing: 6) {
                        HStack(spacing: 10) {
                            RateLegend(down: rateOut, up: rateIn)
                            Spacer(minLength: 0)
                            groupToggle
                        }
                        InlineTrafficChart(series: group.rate_history)
                    }
                    .frame(width: 250)
                } else {
                    groupToggle
                        .fixedSize()
                }
            }

            if !visible.isEmpty {
                VStack(spacing: 0) {
                    ForEach(visible) { t in
                        Divider()
                        memberRow(t)
                    }
                }
                if visible.count < members.count {
                    Text(verbatim: "已按搜索 / 筛选隐藏 \(members.count - visible.count) 个地址")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                }
            } else if members.isEmpty {
                Text("组内暂无通道，点击“编辑”添加目标或把已有通道加入该组")
                    .font(.system(size: 11))
                    .foregroundColor(.secondary)
            }

            Divider()

            HStack(spacing: 12) {
                let session = enabledMembers.reduce(Int64(0)) { $0 + ($1.metrics?.bytes_in ?? 0) + ($1.metrics?.bytes_out ?? 0) }
                let history = members.reduce(Int64(0)) { $0 + ($1.historical_bytes_in ?? 0) + ($1.historical_bytes_out ?? 0) } + session
                HStack(spacing: 16) {
                    cardFooterStat("连接", "\(enabledMembers.reduce(0) { $0 + ($1.metrics?.active_conns ?? 0) })")
                    cardFooterStat("本次", formatBytes(session))
                    cardFooterStat("历史", formatBytes(history))
                    if !showsTraffic && anyEnabled {
                        cardFooterStat("速率", "↑\(formatRate(rateIn)) ↓\(formatRate(rateOut))")
                    }
                }
                .lineLimit(1)

                Spacer(minLength: 8)

                HStack(spacing: 6) {
                    Button(action: onInvite) {
                        Label("邀请", systemImage: "square.and.arrow.up")
                    }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(!anyEnabled)
                    .help(anyEnabled ? "生成整组的邀请链接：同事授权一次即可访问组内全部地址" : "请先开启通道组")

                    Button("连接与授权") { onViewClients(0) }
                        .buttonStyle(.bordered)
                        .controlSize(.small)

                    Button("编辑", action: onEdit)
                        .buttonStyle(.bordered)
                        .controlSize(.small)

                    Button("删除", role: .destructive) { showDeleteDialog = true }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                }
            }
        }
        .padding(14)
        .background(GeometryReader { g in Color.clear.preference(key: CardWidthKey.self, value: g.size.width) })
        .onPreferenceChange(CardWidthKey.self) { cardWidth = $0 }
        .background(RoundedRectangle(cornerRadius: 10).fill(Color(NSColor.controlBackgroundColor)))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Color.primary.opacity(0.09), lineWidth: 1))
        .shadow(color: .black.opacity(0.04), radius: 2, y: 1)
        .opacity(anyEnabled || members.isEmpty ? 1.0 : 0.6)
        .confirmationDialog(Text(verbatim: "删除通道组「\(group.name)」？"), isPresented: $showDeleteDialog, titleVisibility: .visible) {
            Button("解散组，保留 \(members.count) 个通道") {
                Task { await vm.deleteGroup(id: group.id, deleteMembers: false) }
            }
            Button("删除组和组内全部通道", role: .destructive) {
                Task { await vm.deleteGroup(id: group.id, deleteMembers: true) }
            }
            Button("取消", role: .cancel) {}
        } message: {
            Text("组的邀请链接与授权会立即失效。解散后组内通道变回独立通道；删除则一并注销所有端口并断开连接。")
        }
        .alert(Text(verbatim: "删除「\(memberToDelete?.name ?? "")」？"), isPresented: Binding(
            get: { memberToDelete != nil },
            set: { if !$0 { memberToDelete = nil } }
        )) {
            Button("取消", role: .cancel) {}
            Button("确认删除", role: .destructive) {
                if let t = memberToDelete { Task { await vm.deleteTunnel(id: t.id) } }
            }
        } message: {
            Text(verbatim: "将注销 \(vm.publicHost):\(memberToDelete?.listen_port ?? 0) 并断开它的连接，组内其它地址不受影响。")
        }
    }

    // MARK: Parts

    var mappingText: String {
        members.map { "\(vm.publicHost):\($0.listen_port) → \($0.target)" }.joined(separator: "\n")
    }

    var groupToggle: some View {
        Toggle("", isOn: Binding(
            get: { anyEnabled },
            set: { on in Task { await vm.toggleGroup(id: group.id, enabled: on) } }
        ))
        .toggleStyle(.switch)
        .controlSize(.mini)
        .labelsHidden()
        .disabled(members.isEmpty)
        .help(anyEnabled ? "关闭整组" : "开启整组")
    }

    var accessTag: some View {
        let lists = Set(members.map { $0.whitelist.sorted().joined(separator: ",") })
        return Group {
            if members.isEmpty {
                EmptyView()
            } else if members.contains(where: { $0.whitelist.contains("*") }) {
                cardCapsule("exclamationmark.triangle.fill", "含全部放通 *", .red)
            } else if lists.count > 1 {
                cardCapsule("slider.horizontal.3", "成员访问方式不同", .secondary)
            } else if members[0].whitelist.isEmpty {
                cardCapsule("lock.fill", "仅授权链接", .green)
            } else {
                cardCapsule("network", "白名单 + 授权链接", .secondary)
                    .help("固定白名单：" + members[0].whitelist.joined(separator: ", "))
            }
        }
    }

    @ViewBuilder
    var lifecycleItems: some View {
        if anyEnabled {
            let started = enabledMembers.compactMap { $0.started_at }.min()
            cardLifecycleItem("play.circle", "开启 \(formatDateTimeShort(started))")
            if let first = enabledMembers.compactMap({ $0.first_connected_at }).min() {
                cardLifecycleItem("person.wave.2", "首次连接 \(formatDateTimeShort(first))")
            } else {
                cardLifecycleItem("person.wave.2", "尚无连接")
            }
            let connected = enabledMembers.map { $0.activity?.connected_sec ?? $0.connected_sec ?? 0 }.max() ?? 0
            cardLifecycleItem("stopwatch", "实际连接 \(formatSecondsToDuration(connected))", color: .primary, weight: .medium)
                .help("组内连接在线时间最长的一个地址的累计时长")
            let acts = enabledMembers.compactMap { $0.activity }
            if acts.contains(where: { $0.in_use }) {
                cardLifecycleItem("circle.fill", "使用中", color: .green, weight: .medium, iconSize: 7)
                    .help("组内有在线连接、有效授权、待审批申请或可用的邀请链接")
            } else if let act = acts.min(by: { ($0.auto_close_at ?? "~") < ($1.auto_close_at ?? "~") }) {
                if let closeAt = act.auto_close_at {
                    let left = max(0, -secondsSince(closeAt))
                    cardLifecycleItem("moon.zzz", "已闲置 \(formatSecondsToDuration(secondsSince(act.idle_since)))，\(formatSecondsToDuration(left)) 后整组自动关闭",
                                      color: left < 300 ? .orange : .secondary)
                } else {
                    cardLifecycleItem("moon.zzz", "已闲置 \(formatSecondsToDuration(secondsSince(act.idle_since)))（不自动关闭）")
                }
            }
            if let exp = enabledMembers.compactMap({ $0.expires_at }).min() {
                let remaining = formatRemainingTime(expiresAt: exp)
                cardLifecycleItem("hourglass", "最长开启至 \(formatDateTimeShort(exp))", color: remaining.isExpiringSoon ? .orange : .secondary)
            }
        } else if let m = members.first {
            if let reason = m.close_reason, !reason.isEmpty {
                cardLifecycleItem("power", "关闭原因：\(reason)")
            }
            let idle = m.effective_idle_close_minutes ?? 30
            cardLifecycleItem("moon.zzz", "闲置自动关闭：" + (idle > 0 ? idleCloseTitle(idle) : "关"))
        }
    }

    @ViewBuilder
    var badgeItems: some View {
        if !groupPending.isEmpty {
            Button(action: { onViewClients(2) }) {
                cardCapsule("bell.badge.fill", "待审批 \(groupPending.count)", .orange)
            }
            .buttonStyle(.plain)
            .help("有同事申请访问整组，点击审批")
        }
        if !groupLeases.isEmpty {
            Button(action: { onViewClients(2) }) {
                cardCapsule("person.crop.circle.badge.checkmark", "已授权 \(groupLeases.count) 台", .green)
            }
            .buttonStyle(.plain)
            .help("查看与撤销整组的授权")
        }
    }

    func memberRow(_ t: TunnelItem) -> some View {
        let m = t.metrics
        let dot: Color = !t.enabled ? .secondary.opacity(0.4)
            : (m?.status == "online" ? .green : (m?.status == "error" ? .red : .orange))
        let own = (m?.dynamic_leases?.count ?? 0) + (m?.pending?.count ?? 0)
        return HStack(spacing: 8) {
            Circle()
                .fill(dot)
                .frame(width: 7, height: 7)
                .help(t.enabled ? (m?.error ?? "运行中") : "已关闭")
            Text(verbatim: "\(vm.publicHost):\(t.listen_port)")
                .font(.system(size: 12, weight: .medium, design: .monospaced))
                .foregroundColor(t.enabled ? .accentColor : .secondary)
            Image(systemName: "arrow.right")
                .font(.system(size: 9))
                .foregroundColor(.secondary)
            Text(verbatim: t.target)
                .font(.system(size: 12, design: .monospaced))
                .foregroundColor(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
            Menu {
                ForEach(connectSnippets(t, host: vm.publicHost), id: \.cmd) { item in
                    Button(action: { copyToPasteboard(item.cmd) }) {
                        Label(item.title, systemImage: item.icon)
                    }
                }
            } label: {
                Image(systemName: "doc.on.doc")
                    .font(.system(size: 10))
                    .foregroundColor(.secondary)
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
            .help("复制连接命令或访问地址")
            // 批量创建的成员按“组名 (主机)”自动命名，不重复显示；加入组的已有通道保留原名
            if !t.name.hasPrefix(group.name + " (") {
                Text(verbatim: t.name)
                    .font(.system(size: 11))
                    .foregroundColor(.secondary)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
            if own > 0 {
                Button(action: { onMemberClients(t) }) {
                    cardCapsule("person.crop.circle.badge.checkmark", "单独授权 \(own)", .green)
                }
                .buttonStyle(.plain)
                .help("该地址另有单独的授权或申请")
            }
            Spacer(minLength: 8)
            if let n = m?.active_conns, n > 0 {
                Text(verbatim: "\(n) 连接")
                    .font(.system(size: 11))
                    .foregroundColor(.secondary)
                    .monospacedDigit()
            }
            if t.enabled {
                RateText(up: m?.rate_in ?? 0, down: m?.rate_out ?? 0, size: 10)
            }
            Toggle("", isOn: Binding(
                get: { t.enabled },
                set: { on in Task { await vm.toggleTunnel(id: t.id, enabled: on) } }
            ))
            .toggleStyle(.switch)
            .controlSize(.mini)
            .labelsHidden()
            .help(t.enabled ? "单独关闭这个地址" : "单独开启这个地址")
            Menu {
                Button("单独邀请这个地址…") { onInviteMember(t) }
                    .disabled(!t.enabled)
                Button("连接与授权") { onMemberClients(t) }
                Button("编辑…") { onEditMember(t) }
                Button("移出通道组") { Task { _ = await vm.moveTunnel(id: t.id, toGroup: "") } }
                Divider()
                Button("删除…", role: .destructive) { memberToDelete = t }
            } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 13))
                    .foregroundColor(.secondary)
            }
            .menuStyle(.borderlessButton)
            .menuIndicator(.hidden)
            .fixedSize()
        }
        .padding(.vertical, 6)
        .opacity(t.enabled ? 1 : 0.65)
    }

    func ownerChip(_ owner: String) -> some View {
        HStack(spacing: 3) {
            Image(systemName: "person.fill")
                .font(.system(size: 9))
            Text(verbatim: owner)
                .font(.system(size: 11, weight: .medium))
                .lineLimit(1)
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(Color.blue.opacity(0.12))
        .foregroundColor(.blue)
        .cornerRadius(4)
    }
}

// 卡片通用小部件（通道卡片与通道组卡片共用）

func cardCapsule(_ icon: String?, _ text: String, _ color: Color) -> some View {
    HStack(spacing: 3) {
        if let icon = icon {
            Image(systemName: icon)
                .font(.system(size: 9))
        }
        Text(verbatim: text)
            .font(.system(size: 10, weight: .medium))
            .lineLimit(1)
    }
    .padding(.horizontal, 5)
    .padding(.vertical, 1.5)
    .background(color.opacity(0.12))
    .foregroundColor(color == .secondary ? .primary.opacity(0.7) : color)
    .cornerRadius(4)
}

func cardFooterStat(_ label: String, _ value: String) -> some View {
    HStack(spacing: 4) {
        Text(verbatim: label)
            .foregroundColor(.secondary)
        Text(verbatim: value)
            .fontWeight(.medium)
            .foregroundColor(.primary.opacity(0.85))
    }
    .font(.system(size: 11))
    .monospacedDigit()
}

func cardLifecycleItem(_ icon: String, _ text: String, color: Color = .secondary, weight: Font.Weight = .regular, iconSize: CGFloat = 11) -> some View {
    HStack(spacing: 4) {
        Image(systemName: icon)
            .font(.system(size: iconSize))
        Text(verbatim: text)
            .font(.system(size: 11, weight: weight))
            .monospacedDigit()
    }
    .foregroundColor(color)
}

// MARK: - Client Details & Historical Records Sheet

/// 客户明细弹窗的目标（通道或通道组 + 初始页签），整体作为 sheet(item:) 的参数
struct ClientsSheetTarget: Identifiable {
    let scope: AccessScope
    let tab: Int
    var id: String { "\(scope.key)#\(tab)" }

    init(scope: AccessScope, tab: Int) {
        self.scope = scope
        self.tab = tab
    }

    init(tunnel: TunnelItem, tab: Int) {
        self.init(scope: .tunnel(tunnel.id), tab: tab)
    }
}

struct ClientDetailsSheet: View {
    @ObservedObject var vm: PortRelayViewModel
    let scope: AccessScope
    @Binding var isPresented: Bool

    @State private var selectedTab: Int
    @State private var revokeError: String? = nil
    @State private var expandedIPs: Set<String> = []
    @State private var expandedHistoryIPs: Set<String> = []
    @State private var isRevoking: Bool = false

    init(vm: PortRelayViewModel, scope: AccessScope, initialTab: Int = 0, isPresented: Binding<Bool>) {
        _vm = ObservedObject(wrappedValue: vm)
        self.scope = scope
        _isPresented = isPresented
        _selectedTab = State(initialValue: initialTab)
    }

    init(vm: PortRelayViewModel, tunnel: TunnelItem, initialTab: Int = 0, isPresented: Binding<Bool>) {
        self.init(vm: vm, scope: .tunnel(tunnel.id), initialTab: initialTab, isPresented: isPresented)
    }

    // 弹窗打开期间始终读取最新数据（授权列表每秒刷新）
    var members: [TunnelItem] { vm.scopeTunnels(scope) }
    var memberIDs: Set<String> { Set(members.map { $0.id }) }
    var leases: [DynamicLeaseItem] { vm.scopeLeases(scope) }
    var invites: [InviteItem] { vm.scopeInvites(scope) }
    var pendingRequests: [AccessRequestItem] { vm.scopePending(scope) }

    var subtitle: String {
        if scope.isGroup {
            let ports = members.map { $0.listen_port }
            guard let lo = ports.min(), let hi = ports.max() else { return "组内暂无通道" }
            return "\(members.count) 个地址 · \(vm.publicHost):\(lo)" + (hi > lo ? "–\(hi)" : "")
        }
        guard let t = members.first else { return "" }
        return "\(vm.publicHost):\(t.listen_port) → \(t.target)"
    }

    /// 通道组：这台设备连到了组内哪些地址
    func groupTargetsSummary(_ conns: [ActiveClientInfo]) -> String {
        guard scope.isGroup else { return "" }
        let targets = Set(conns.compactMap { targetLabel($0.tunnel_id) })
        if targets.count == 1, let only = targets.first { return " · \(only)" }
        return " · 连到 \(targets.count) 个地址"
    }

    /// 通道组里每个连接经过的目标（"→ 10.0.1.11:22"）
    func targetLabel(_ tunnelID: String) -> String? {
        guard scope.isGroup, let t = members.first(where: { $0.id == tunnelID }) else { return nil }
        return "→ \(t.target)"
    }

    @State private var showingInviteSheet = false
    @State private var copiedCode: String? = nil
    @State private var historyRecords: [ClientHistoryRecord] = []
    @State private var isLoadingHistory: Bool = false

    var activeClientsForTunnel: [ActiveClientInfo] {
        let ids = memberIDs
        return vm.activeConns.filter { ids.contains($0.tunnel_id) }
    }

    var historyGroups: [HistoryIPGroup] {
        Dictionary(grouping: historyRecords) { hostOf($0.client_ip) }
            .map { HistoryIPGroup(ip: $0.key, records: $0.value.sorted { $0.connected_at > $1.connected_at }) }
            .sorted { ($0.lastSeen ?? "") > ($1.lastSeen ?? "") }
    }

    var clientGroups: [ClientIPGroup] {
        Dictionary(grouping: activeClientsForTunnel) { hostOf($0.client_ip) }
            .map { ClientIPGroup(ip: $0.key, conns: $0.value.sorted { $0.connected_at < $1.connected_at }) }
            .sorted { $0.conns.count != $1.conns.count ? $0.conns.count > $1.conns.count : $0.ip < $1.ip }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            // Header
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: "\(vm.scopeTitle(scope)) · 连接与授权")
                        .font(.system(size: 15, weight: .bold))
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Text(verbatim: subtitle)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                }
                Spacer()
                Button("完成") { isPresented = false }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
            }

            // Tab Picker
            Picker("", selection: $selectedTab) {
                Text(verbatim: clientGroups.isEmpty ? "当前在线客户端" : "当前在线 (\(clientGroups.count) 台 · \(activeClientsForTunnel.count) 连接)").tag(0)
                Text(verbatim: historyGroups.isEmpty ? "历史接入台账" : "历史接入台账 (\(historyGroups.count) 台)").tag(1)
                Text(verbatim: "授权管理 (\(leases.count + pendingRequests.count))").tag(2)
            }
            .pickerStyle(.segmented)
            .padding(.vertical, 4)

            Divider()

            // Tab Content
            if selectedTab == 0 {
                // Live Connected Clients
                if activeClientsForTunnel.isEmpty {
                    VStack(spacing: 8) {
                        Image(systemName: "person.slash")
                            .font(.system(size: 28))
                            .foregroundColor(.secondary)
                        Text("当前暂无客户端连接此端口")
                            .font(.system(size: 13))
                            .foregroundColor(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    ScrollView {
                        VStack(spacing: 8) {
                            ForEach(clientGroups) { g in
                                clientGroupRow(g)
                            }
                        }
                        .padding(.bottom, 8)
                    }
                }
            } else if selectedTab == 1 {
                // Historical Connections Log Table
                if isLoadingHistory {
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if historyRecords.isEmpty {
                    VStack(spacing: 8) {
                        Image(systemName: "doc.text")
                            .font(.system(size: 28))
                            .foregroundColor(.secondary)
                        Text("暂无已断开的连接历史记录")
                            .font(.system(size: 13))
                            .foregroundColor(.secondary)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    ScrollView {
                        VStack(spacing: 8) {
                            ForEach(historyGroups) { g in
                                historyGroupRow(g)
                            }
                        }
                        .padding(.bottom, 8)
                    }
                }
            } else {
                leasesTab
            }
        }
        .padding(20)
        .frame(minWidth: 720, idealWidth: 760, maxWidth: .infinity, minHeight: 540, idealHeight: 600, maxHeight: .infinity)
        .onAppear {
            loadHistory()
        }
        .onChange(of: selectedTab) { newTab in
            if newTab == 1 { loadHistory() }
        }
    }

    /// 只有一个 IP 时默认展开；点击切换展开状态
    func isExpanded(_ ip: String, in set: Set<String>, groupCount: Int) -> Bool {
        (groupCount == 1) != set.contains(ip)
    }

    func toggle(_ ip: String, in set: inout Set<String>) {
        if set.contains(ip) { set.remove(ip) } else { set.insert(ip) }
    }

    func chevron(_ expanded: Bool, visible: Bool = true) -> some View {
        Image(systemName: "chevron.right")
            .font(.system(size: 10, weight: .semibold))
            .foregroundColor(.secondary)
            .rotationEffect(.degrees(expanded ? 90 : 0))
            .frame(width: 12)
            .opacity(visible ? 1 : 0)
    }

    @ViewBuilder
    func historyGroupRow(_ g: HistoryIPGroup) -> some View {
        let expanded = isExpanded(g.ip, in: expandedHistoryIPs, groupCount: historyGroups.count)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                chevron(expanded)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text(verbatim: g.ip)
                            .font(.system(size: 13, weight: .semibold, design: .monospaced))
                            .lineLimit(1)
                            .truncationMode(.middle)
                        TagView("\(g.records.count) 次连接")
                    }
                    Text(verbatim: "最近 \(formatDateTimeShort(g.lastSeen)) · 首次 \(formatDateTimeShort(g.firstSeen)) · 累计 \(formatSecondsToDuration(g.totalDuration))")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                }
                .layoutPriority(1)
                Spacer(minLength: 8)
                VStack(alignment: .trailing, spacing: 3) {
                    Text(verbatim: formatBytes(g.bytesIn + g.bytesOut))
                        .font(.system(size: 13, weight: .semibold, design: .monospaced))
                    Text(verbatim: "↑ \(formatBytes(g.bytesIn))  ↓ \(formatBytes(g.bytesOut))")
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(.secondary)
                }
                .lineLimit(1)
                .frame(width: 190, alignment: .trailing)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .contentShape(Rectangle())
            .onTapGesture { toggle(g.ip, in: &expandedHistoryIPs) }

            if expanded {
                Divider().padding(.leading, 32).padding(.trailing, 12)
                VStack(spacing: 0) {
                    ForEach(g.records) { rec in
                        HStack(spacing: 8) {
                            Color.clear.frame(width: 12)
                            Text(verbatim: ":\(portOf(rec.client_ip))")
                                .font(.system(size: 11, design: .monospaced))
                                .frame(width: 56, alignment: .leading)
                            Text(verbatim: "\(formatDateTimeShort(rec.connected_at)) – \(formatTimeHHmm(rec.disconnected_at))")
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 180, alignment: .leading)
                            Text(verbatim: formatSecondsToDuration(rec.duration_sec))
                                .font(.system(size: 11))
                                .foregroundColor(.secondary)
                                .frame(width: 80, alignment: .leading)
                            if let to = targetLabel(rec.tunnel_id) {
                                Text(verbatim: to)
                                    .font(.system(size: 11, design: .monospaced))
                                    .foregroundColor(.secondary)
                                    .truncationMode(.middle)
                            }
                            Spacer(minLength: 8)
                            Text(verbatim: "↑ \(formatBytes(rec.bytes_in))  ↓ \(formatBytes(rec.bytes_out))")
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 190, alignment: .trailing)
                        }
                        .lineLimit(1)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 5)
                    }
                }
                .padding(.vertical, 4)
            }
        }
        .rowCard()
    }

    func isLeased(_ ip: String) -> Bool {
        leases.contains { $0.ip == ip }
    }

    func leaseName(_ ip: String) -> String? {
        guard let n = leases.first(where: { $0.ip == ip })?.name, !n.isEmpty else { return nil }
        return n
    }

    @ViewBuilder
    func clientGroupRow(_ g: ClientIPGroup) -> some View {
        let multi = g.conns.count > 1
        let expanded = multi && isExpanded(g.ip, in: expandedIPs, groupCount: clientGroups.count)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                chevron(expanded, visible: multi)
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 6) {
                        Text(verbatim: g.ip)
                            .font(.system(size: 13, weight: .semibold, design: .monospaced))
                            .foregroundColor(.accentColor)
                            .lineLimit(1)
                            .truncationMode(.middle)
                        TagView("\(g.conns.count) 个连接")
                        if isLeased(g.ip) {
                            TagView(leaseName(g.ip).map { "已授权 · \($0)" } ?? "已授权", color: .green)
                        }
                    }
                    Text(verbatim: "最早接入 \(formatDateTimeShort(g.firstConnectedAt)) · ↑ \(formatBytes(g.bytesIn))  ↓ \(formatBytes(g.bytesOut))"
                         + groupTargetsSummary(g.conns))
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                }
                .layoutPriority(1)
                Spacer(minLength: 8)
                RateText(up: g.rateIn, down: g.rateOut)
                Button(multi ? "全部断开" : "断开", role: .destructive) {
                    Task { await vm.killClients(connIds: g.conns.map { $0.conn_id }) }
                }
                .controlSize(.small)
                .frame(width: 76, alignment: .trailing)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 10)
            .contentShape(Rectangle())
            .onTapGesture { if multi { toggle(g.ip, in: &expandedIPs) } }

            if expanded {
                Divider().padding(.leading, 32).padding(.trailing, 12)
                VStack(spacing: 0) {
                    ForEach(g.conns) { c in
                        HStack(spacing: 8) {
                            Color.clear.frame(width: 12)
                            // 通道组里更关心连到了哪个目标，来源端口让位给目标地址
                            if !scope.isGroup {
                                Text(verbatim: ":\(portOf(c.client_ip))")
                                    .font(.system(size: 11, design: .monospaced))
                                    .frame(width: 56, alignment: .leading)
                            }
                            Text(verbatim: "接入 \(formatTimeHHmm(c.connected_at))")
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundColor(.secondary)
                                .frame(width: 104, alignment: .leading)
                            Text(verbatim: "↑ \(formatBytes(c.bytes_in))  ↓ \(formatBytes(c.bytes_out))")
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundColor(.secondary)
                                .fixedSize()
                            if let to = targetLabel(c.tunnel_id) {
                                Text(verbatim: to)
                                    .font(.system(size: 11, design: .monospaced))
                                    .foregroundColor(.secondary)
                                    .truncationMode(.middle)
                            }
                            Spacer(minLength: 8)
                            RateText(up: c.rate_in, down: c.rate_out, size: 10)
                            Button("断开") {
                                Task { await vm.killClient(connId: c.conn_id) }
                            }
                            .buttonStyle(.borderless)
                            .foregroundColor(.red)
                            .font(.system(size: 11))
                            .frame(width: 76, alignment: .trailing)
                        }
                        .lineLimit(1)
                        .padding(.horizontal, 12)
                        .padding(.vertical, 5)
                    }
                }
                .padding(.vertical, 4)
            }
        }
        .rowCard()
    }

    func sectionHeader(_ title: String, _ count: Int) -> some View {
        Text(verbatim: "\(title) · \(count)")
            .font(.system(size: 12, weight: .semibold))
            .foregroundColor(.secondary)
            .padding(.top, 6)
    }

    func emptyHint(_ text: String) -> some View {
        Text(verbatim: text)
            .font(.system(size: 11))
            .foregroundColor(.secondary)
            .padding(.vertical, 2)
    }

    var leasesTab: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .center, spacing: 8) {
                Label {
                    Text(verbatim: scope.isGroup
                         ? "组内所有地址共用邀请与授权：同事授权一次即可访问整组，计时从首次连上任一地址开始"
                         : "同事打开邀请链接并填写姓名后获得访问权限；可访问时长与是否需要审批在每个邀请中设置")
                } icon: {
                    Image(systemName: "link")
                }
                .font(.system(size: 11))
                .foregroundColor(.secondary)
                .lineLimit(1)
                Spacer()
                if !leases.isEmpty {
                    Button("全部撤销", role: .destructive) { revoke(ip: nil) }
                        .controlSize(.small)
                        .disabled(isRevoking)
                }
                Button {
                    showingInviteSheet = true
                } label: {
                    Label("新建邀请", systemImage: "plus")
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .disabled(!vm.scopeEnabled(scope))
                .help(vm.scopeEnabled(scope) ? "生成新的邀请链接" : "请先开启通道")
            }

            if let err = revokeError {
                Label(err, systemImage: "exclamationmark.triangle.fill")
                    .font(.system(size: 11))
                    .foregroundColor(.red)
            }

            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    if !pendingRequests.isEmpty {
                        sectionHeader("待审批", pendingRequests.count)
                        ForEach(pendingRequests) { r in
                            HStack(spacing: 10) {
                                Image(systemName: "bell.badge.fill")
                                    .foregroundColor(.orange)
                                VStack(alignment: .leading, spacing: 3) {
                                    HStack(spacing: 6) {
                                        Text(verbatim: r.name)
                                            .font(.system(size: 13, weight: .semibold))
                                            .lineLimit(1)
                                        Text(verbatim: r.ip)
                                            .font(.system(size: 12, design: .monospaced))
                                            .foregroundColor(.accentColor)
                                    }
                                    Text(verbatim: "申请于 \(formatDateTimeShort(r.at)) · 批准后可访问 \(leaseDurationTitle(invites.first { $0.code == r.invite }?.lease_duration))")
                                        .font(.system(size: 11))
                                        .foregroundColor(.secondary)
                                        .lineLimit(1)
                                }
                                .layoutPriority(1)
                                Spacer(minLength: 8)
                                Button("拒绝") { decide(r, approve: false) }
                                    .controlSize(.small)
                                    .disabled(isRevoking)
                                Button("批准") { decide(r, approve: true) }
                                    .buttonStyle(.borderedProminent)
                                    .controlSize(.small)
                                    .disabled(isRevoking)
                            }
                            .padding(.horizontal, 12)
                            .padding(.vertical, 10)
                            .rowCard(tint: .orange)
                        }
                    }

                    sectionHeader("已授权设备", leases.count)
                    if leases.isEmpty {
                        emptyHint("暂无。点击“新建邀请”把链接发给同事，对方打开并填写姓名后即可访问。")
                    }
                    ForEach(leases) { lease in
                        let remaining = formatRemainingTime(expiresAt: lease.expires_at)
                        let online = vm.activeConns.filter { memberIDs.contains($0.tunnel_id) && hostOf($0.client_ip) == lease.ip }.count
                        HStack(spacing: 10) {
                            Circle()
                                .fill(online > 0 ? Color.green : Color.secondary.opacity(0.35))
                                .frame(width: 7, height: 7)
                                .help(online > 0 ? "在线" : "当前没有连接")
                            VStack(alignment: .leading, spacing: 3) {
                                HStack(spacing: 6) {
                                    Text(verbatim: lease.name?.isEmpty == false ? lease.name! : "未署名")
                                        .font(.system(size: 13, weight: .semibold))
                                        .lineLimit(1)
                                        .truncationMode(.tail)
                                    Text(verbatim: lease.ip)
                                        .font(.system(size: 12, design: .monospaced))
                                        .foregroundColor(.accentColor)
                                        .lineLimit(1)
                                    if online > 0 {
                                        TagView("在线 \(online) 个连接", color: .green)
                                    }
                                }
                                if lease.awaiting_connect == true {
                                    Text(verbatim: "已授权，尚未连接 · 首次连接后可访问 \(leaseDurationTitle(lease.duration)) · 请在 \(formatDateTimeShort(lease.expires_at)) 前连接")
                                        .font(.system(size: 11))
                                        .foregroundColor(.secondary)
                                        .lineLimit(1)
                                } else {
                                    Text(verbatim: (lease.started_at.map { "首次连接 \(formatDateTimeShort($0)) · " } ?? "") + "有效期至 \(formatDateTimeShort(lease.expires_at)) · \(remaining.text)")
                                        .font(.system(size: 11))
                                        .foregroundColor(remaining.isExpiringSoon ? .orange : .secondary)
                                        .lineLimit(1)
                                }
                            }
                            .layoutPriority(1)
                            Spacer(minLength: 8)
                            Button("撤销", role: .destructive) { revoke(ip: lease.ip) }
                                .controlSize(.small)
                                .disabled(isRevoking)
                                .help("立即收回该设备的访问权限并断开连接")
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 10)
                        .rowCard()
                    }

                    sectionHeader("有效的邀请链接", invites.count)
                    if invites.isEmpty {
                        emptyHint("暂无有效的邀请链接")
                    }
                    ForEach(invites) { inv in
                        let used = inv.uses ?? []
                        let full = inv.max_uses > 0 && used.count >= inv.max_uses
                        HStack(spacing: 10) {
                            Image(systemName: "link")
                                .foregroundColor(.secondary)
                            VStack(alignment: .leading, spacing: 3) {
                                HStack(spacing: 6) {
                                    Text(verbatim: inv.note?.isEmpty == false ? "给 \(inv.note!)" : "未备注的邀请")
                                        .font(.system(size: 13, weight: .semibold))
                                        .lineLimit(1)
                                    TagView("已用 \(used.count)/\(inv.max_uses > 0 ? "\(inv.max_uses)" : "不限")", color: full ? .orange : .secondary)
                                    TagView("授权后可访问 \(leaseDurationTitle(inv.lease_duration))", color: .blue)
                                    if inv.require_approval == true {
                                        TagView("需审批", color: .orange)
                                    }
                                }
                                Text(verbatim: "链接有效至 \(formatDateTimeShort(inv.expires_at))" + (used.isEmpty ? " · 尚未使用" : " · \(used.map { $0.name }.joined(separator: "、"))"))
                                    .font(.system(size: 11))
                                    .foregroundColor(.secondary)
                                    .lineLimit(1)
                                    .truncationMode(.tail)
                            }
                            .layoutPriority(1)
                            Spacer(minLength: 8)
                            Button(copiedCode == inv.code ? "已复制 ✓" : "复制分享文案") {
                                Task {
                                    let (resp, _) = await vm.inviteShare(scope: scope, code: inv.code)
                                    copyToPasteboard(resp?.share_text ?? (vm.inviteBase + inv.code))
                                    copiedCode = inv.code
                                }
                            }
                            .controlSize(.small)
                            .disabled(full)
                            .help(full ? "该链接已用满，请新建邀请" : "复制可直接发给同事的文案")
                            Button("作废", role: .destructive) {
                                isRevoking = true
                                Task {
                                    revokeError = await vm.revokeInvite(scope: scope, code: inv.code)
                                    isRevoking = false
                                }
                            }
                            .controlSize(.small)
                            .disabled(isRevoking)
                            .help("作废后链接失效，通过它获得的访问权限也会被收回")
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 10)
                        .rowCard()
                    }
                }
                .padding(.bottom, 8)
            }
        }
        .sheet(isPresented: $showingInviteSheet) {
            InviteSheet(vm: vm, scope: scope, autoGenerate: false, isPresented: $showingInviteSheet)
        }
    }

    func decide(_ r: AccessRequestItem, approve: Bool) {
        isRevoking = true
        Task {
            revokeError = await vm.decideRequest(scope: scope, requestId: r.id, approve: approve)
            isRevoking = false
        }
    }

    func revoke(ip: String?) {
        isRevoking = true
        revokeError = nil
        Task {
            revokeError = await vm.revokeLease(scope: scope, ip: ip)
            isRevoking = false
        }
    }

    func loadHistory() {
        isLoadingHistory = true
        Task {
            let list = await vm.fetchHistory(scope: scope)
            historyRecords = list
            isLoadingHistory = false
        }
    }
}

// MARK: - Diagnostics & Permission Status Sheet (Live Check)

struct DiagnosticsSheet: View {
    @ObservedObject var vm: PortRelayViewModel
    @Binding var isPresented: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            // Header
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("系统权限与网络环境诊断")
                        .font(.system(size: 15, weight: .bold))
                    Text("检测本机网络授权、中继服务器通信与后台引擎状态")
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                }
                Spacer()
                Button(action: { vm.runDiagnostics() }) {
                    Label("重新检测", systemImage: "arrow.clockwise")
                }
                .buttonStyle(.bordered)
                .controlSize(.small)
                
                Button("完成") { isPresented = false }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
            }

            Divider()

            // Diagnostic Items List
            VStack(spacing: 10) {
                ForEach(vm.diagnostics) { item in
                    HStack(alignment: .top, spacing: 12) {
                        Group {
                            switch item.status {
                            case .ok:
                                Image(systemName: "checkmark.circle.fill")
                                    .foregroundColor(.green)
                            case .warning:
                                Image(systemName: "exclamationmark.triangle.fill")
                                    .foregroundColor(.orange)
                            case .error:
                                Image(systemName: "xmark.circle.fill")
                                    .foregroundColor(.red)
                            case .checking:
                                ProgressView().controlSize(.small)
                            }
                        }
                        .font(.system(size: 16))
                        .frame(width: 20)

                        VStack(alignment: .leading, spacing: 4) {
                            HStack {
                                Text(item.title)
                                    .font(.system(size: 13, weight: .semibold))
                                Spacer()
                                Text(item.statusText)
                                    .font(.system(size: 11, weight: .medium))
                                    .foregroundColor(item.status == .ok ? .green : (item.status == .error ? .red : .orange))
                                    .padding(.horizontal, 6)
                                    .padding(.vertical, 1.5)
                                    .background((item.status == .ok ? Color.green : (item.status == .error ? Color.red : Color.orange)).opacity(0.12))
                                    .cornerRadius(4)
                            }

                            Text(item.desc)
                                .font(.system(size: 12))
                                .foregroundColor(.secondary)
                                .lineSpacing(2)

                            if let actionTitle = item.actionTitle, let action = item.action {
                                Button(actionTitle, action: action)
                                    .buttonStyle(.link)
                                    .font(.system(size: 12))
                                    .padding(.top, 2)
                            }
                        }
                    }
                    .padding(12)
                    .rowCard(tint: item.status == .error ? .red : (item.status == .warning ? .orange : nil))
                }
            }

            Spacer()

            Text("说明：若 macOS 弹出「PortRelay 想要查找并连接本地网络上的设备」提示，请选择「允许」。")
                .font(.system(size: 11))
                .foregroundColor(.secondary)
        }
        .padding(20)
        .frame(width: 580, height: 460)
    }
}

// MARK: - Edit / Create Sheet (With Owner & Duration TTL)

struct TunnelEditSheet: View {
    @ObservedObject var vm: PortRelayViewModel
    let tunnel: TunnelItem?
    @Binding var isPresented: Bool

    @State private var name: String
    @State private var owner: String
    @State private var target: String
    @State private var portString: String
    @State private var durationMinutes: Int
    @State private var idleCloseMinutes: Int
    @State private var maxBandwidthMB: Double // 0 为不限速
    @State private var whitelistString: String
    @State private var accessMode: AccessMode
    @State private var groupId: String
    @State private var probeMessage: String = ""
    @State private var isProbing: Bool = false
    @State private var errorMessage: String? = nil

    var onCreated: ((String) -> Void)? = nil

    enum AccessMode: String, CaseIterable {
        case linkOnly, specific, all
    }

    var isEdit: Bool { tunnel != nil }

    /// 表单字段在创建时就用通道数据填好，不依赖 onAppear（避免第一帧显示空表单）
    init(vm: PortRelayViewModel, tunnel: TunnelItem?, isPresented: Binding<Bool>, onCreated: ((String) -> Void)? = nil) {
        _vm = ObservedObject(wrappedValue: vm)
        self.tunnel = tunnel
        self.onCreated = onCreated
        _isPresented = isPresented
        let wl = tunnel?.whitelist ?? []
        let mode: AccessMode = wl.isEmpty ? .linkOnly : (wl.contains("*") ? .all : .specific)
        _accessMode = State(initialValue: mode)
        _name = State(initialValue: tunnel?.name ?? "")
        _owner = State(initialValue: tunnel.map { $0.owner ?? "" } ?? NSUserName())
        _target = State(initialValue: tunnel?.target ?? "")
        _portString = State(initialValue: tunnel.map { "\($0.listen_port)" } ?? "")
        // duration_minutes 为 0 时 JSON 中会省略，缺省即“不限制”；新建默认 2 小时
        // 最长开启时长：新建默认不限制（主要依靠闲置自动关闭）
        _durationMinutes = State(initialValue: tunnel.map { $0.duration_minutes ?? 0 } ?? 0)
        _idleCloseMinutes = State(initialValue: tunnel?.effective_idle_close_minutes ?? 30)
        _maxBandwidthMB = State(initialValue: tunnel?.max_bandwidth_mb ?? 0)
        _whitelistString = State(initialValue: mode == .specific ? wl.joined(separator: ", ") : "")
        _groupId = State(initialValue: tunnel?.group_id ?? "")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(isEdit ? "编辑通道" : "添加通道")
                .font(.system(size: 15, weight: .bold))

            HStack(spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("通道名称")
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                    TextField("例如：MySQL 实习库", text: $name)
                        .textFieldStyle(.roundedBorder)
                }

                VStack(alignment: .leading, spacing: 4) {
                    Text("关联责任人")
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                    TextField("例如：张三 / 数据组", text: $owner)
                        .textFieldStyle(.roundedBorder)
                }
            }

            if !vm.groups.isEmpty {
                HStack(spacing: 8) {
                    Text("所属通道组")
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                    Picker("", selection: $groupId) {
                        Text("不属于任何组").tag("")
                        ForEach(vm.groups) { g in
                            Text(verbatim: g.name).tag(g.id)
                        }
                    }
                    .labelsHidden()
                    .frame(maxWidth: 260)
                    Spacer()
                }
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("目标服务 (内网主机 IP:端口)")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                HStack {
                    TextField("例如 10.0.0.10:3306", text: $target)
                        .textFieldStyle(.roundedBorder)
                    Button(isProbing ? "测试中" : "连通性测试") {
                        Task {
                            isProbing = true
                            let (_, msg) = await vm.probeTarget(target: target)
                            probeMessage = msg
                            isProbing = false
                        }
                    }
                    .disabled(target.isEmpty || isProbing)
                    .controlSize(.small)
                }
                if !probeMessage.isEmpty {
                    Text(probeMessage)
                        .font(.system(size: 11))
                        .foregroundColor(probeMessage.contains("成功") ? .green : .red)
                }
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("对外监听端口 (范围 33001-34000)")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                HStack {
                    TextField("33660", text: $portString)
                        .textFieldStyle(.roundedBorder)
                    Button("自动分配") {
                        Task {
                            let p = await vm.recommendPort()
                            portString = "\(p)"
                        }
                    }
                    .controlSize(.small)
                }
            }

            // Auto close: idle (primary) + optional hard cap
            VStack(alignment: .leading, spacing: 6) {
                Text("闲置自动关闭（没有连接、有效授权、待审批和可用邀请时开始计时）")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                HStack(spacing: 6) {
                    ForEach(idleCloseOptions, id: \.0) { opt in
                        DurationChip(title: opt.1, minutes: opt.0, selectedMinutes: $idleCloseMinutes)
                    }
                }
                Text("最长开启时长（可选，到时无论是否在用都会关闭）")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                    .padding(.top, 2)
                HStack(spacing: 6) {
                    DurationChip(title: "不限制", minutes: 0, selectedMinutes: $durationMinutes)
                    DurationChip(title: "2 小时", minutes: 120, selectedMinutes: $durationMinutes)
                    DurationChip(title: "8 小时", minutes: 480, selectedMinutes: $durationMinutes)
                    DurationChip(title: "1 天", minutes: 1440, selectedMinutes: $durationMinutes)
                    DurationChip(title: "7 天", minutes: 10080, selectedMinutes: $durationMinutes)
                    // 兼容旧配置中的其他时长
                    if ![0, 120, 480, 1440, 10080].contains(durationMinutes) {
                        DurationChip(title: formatSecondsToDuration(Int64(durationMinutes) * 60), minutes: durationMinutes, selectedMinutes: $durationMinutes)
                    }
                }
            }

            // QoS Bandwidth Limit Option
            VStack(alignment: .leading, spacing: 4) {
                Text("单通道带宽限速 (QoS 防挤占)")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                
                HStack(spacing: 6) {
                    BandwidthChip(title: "不限速", mb: 0, selectedMB: $maxBandwidthMB)
                    BandwidthChip(title: "2 MB/s", mb: 2, selectedMB: $maxBandwidthMB)
                    BandwidthChip(title: "5 MB/s", mb: 5, selectedMB: $maxBandwidthMB)
                    BandwidthChip(title: "10 MB/s", mb: 10, selectedMB: $maxBandwidthMB)
                    BandwidthChip(title: "20 MB/s", mb: 20, selectedMB: $maxBandwidthMB)
                }
            }

            VStack(alignment: .leading, spacing: 6) {
                Text("访问控制")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                Picker("", selection: $accessMode) {
                    Text("仅授权链接（推荐）").tag(AccessMode.linkOnly)
                    Text("固定 IP / 网段").tag(AccessMode.specific)
                    Text("全部放通 *").tag(AccessMode.all)
                }
                .pickerStyle(.segmented)
                .labelsHidden()

                switch accessMode {
                case .linkOnly:
                    Text("只有通过邀请链接授权的设备可以访问，保存后可以直接设置并生成邀请链接。")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                case .specific:
                    TextField("例如 192.168.1.100, 192.168.1.0/24（逗号分隔）", text: $whitelistString)
                        .textFieldStyle(.roundedBorder)
                    Text("名单内的设备始终可以访问；其他人仍可通过邀请链接临时授权。")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                case .all:
                    Label("任何能访问服务器的设备都可以连接，不建议用于数据库等敏感服务。", systemImage: "exclamationmark.triangle.fill")
                        .font(.system(size: 11))
                        .foregroundColor(.red)
                }
            }

            if let err = errorMessage {
                Text(err)
                    .font(.system(size: 12))
                    .foregroundColor(.red)
            }

            HStack {
                Spacer()
                Button("取消") { isPresented = false }
                    .controlSize(.small)
                Button("保存") {
                    guard let p = Int(portString), p > 33000 && p <= 34000 else {
                        errorMessage = "端口须在 33001-34000 范围内"
                        return
                    }
                    var wl: [String] = []
                    switch accessMode {
                    case .linkOnly:
                        wl = []
                    case .all:
                        wl = ["*"]
                    case .specific:
                        wl = whitelistString.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
                        if wl.isEmpty {
                            errorMessage = "请填写允许访问的 IP 或网段，或选择“仅授权链接”"
                            return
                        }
                        if wl.contains("*") {
                            errorMessage = "如需放通全部，请选择“全部放通 *”"
                            return
                        }
                    }
                    Task {
                        let (ok, err, savedId) = await vm.saveTunnel(
                            id: tunnel?.id,
                            name: name,
                            target: target,
                            port: p,
                            whitelist: wl,
                            owner: owner.trimmingCharacters(in: .whitespaces),
                            durationMinutes: durationMinutes,
                            idleCloseMinutes: idleCloseMinutes,
                            maxBandwidthMB: maxBandwidthMB,
                            groupId: groupId
                        )
                        if ok {
                            isPresented = false
                            if !isEdit, accessMode != .all, let id = savedId {
                                onCreated?(id)
                            }
                        } else {
                            errorMessage = err ?? "保存失败"
                        }
                    }
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
                .disabled(name.isEmpty || target.isEmpty)
            }
            .padding(.top, 4)
        }
        .padding(20)
        .frame(width: 490)
        .onAppear {
            if tunnel == nil {
                Task {
                    let p = await vm.recommendPort()
                    if portString.isEmpty { portString = "\(p)" }
                }
            }
        }
    }
}

// MARK: - Tunnel Group Edit Sheet

struct GroupSheetTarget: Identifiable {
    let group: GroupItem?
    var id: String { group?.id ?? "new" }
}

/// 新建 / 编辑通道组：批量添加“主机 × 端口”，也可以把已有通道加入该组
struct GroupEditSheet: View {
    @ObservedObject var vm: PortRelayViewModel
    let group: GroupItem?
    @Binding var isPresented: Bool
    var onCreated: ((String) -> Void)? = nil

    @State private var name: String
    @State private var owner: String
    @State private var hosts: String = ""
    @State private var ports: String = ""
    @State private var moveIDs: Set<String> = []
    @State private var idleCloseMinutes: Int = 30
    @State private var durationMinutes: Int = 0
    @State private var accessMode: TunnelEditSheet.AccessMode = .linkOnly
    @State private var whitelistString: String = ""
    @State private var preview: [GroupPreviewMember] = []
    @State private var previewError: String? = nil
    @State private var errorMessage: String? = nil
    @State private var isSaving = false

    init(vm: PortRelayViewModel, group: GroupItem?, isPresented: Binding<Bool>, onCreated: ((String) -> Void)? = nil,
         initialName: String = "", initialHosts: String = "", initialPorts: String = "") {
        _vm = ObservedObject(wrappedValue: vm)
        self.group = group
        self.onCreated = onCreated
        _isPresented = isPresented
        _name = State(initialValue: group?.name ?? initialName)
        _owner = State(initialValue: group.map { $0.owner ?? "" } ?? NSUserName())
        _hosts = State(initialValue: initialHosts)
        _ports = State(initialValue: initialPorts)
    }

    var isEdit: Bool { group != nil }

    /// 可以加入该组的已有通道（不属于任何组）
    var candidates: [TunnelItem] {
        vm.tunnels.filter { ($0.group_id ?? "").isEmpty }
    }

    var hasNewTargets: Bool { !hosts.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var canSave: Bool {
        !name.trimmingCharacters(in: .whitespaces).isEmpty && !isSaving
            && (isEdit || hasNewTargets || !moveIDs.isEmpty)
            && (!hasNewTargets || (previewError == nil && !preview.isEmpty))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 2) {
                Text(isEdit ? "编辑通道组" : "新建通道组")
                    .font(.system(size: 15, weight: .bold))
                Text("组内地址共用邀请与授权：同事授权一次即可访问整组，组内任一地址在用时整组都不会闲置关闭")
                    .font(.system(size: 11))
                    .foregroundColor(.secondary)
            }

            HStack(spacing: 12) {
                field("通道组名称") {
                    TextField("例如：订单服务集群", text: $name)
                        .textFieldStyle(.roundedBorder)
                }
                field("关联责任人") {
                    TextField("例如：张三 / 运维组", text: $owner)
                        .textFieldStyle(.roundedBorder)
                }
            }

            VStack(alignment: .leading, spacing: 6) {
                Text(isEdit ? "追加目标（可选）" : "目标主机与端口")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                HStack(alignment: .top, spacing: 8) {
                    TextField("主机：10.0.1.11-13，或 10.0.1.11, 10.0.1.12，也可直接写 主机:端口", text: $hosts)
                        .textFieldStyle(.roundedBorder)
                    TextField("端口：22, 8080", text: $ports)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 130)
                }
                if let err = previewError, hasNewTargets {
                    Text(verbatim: err)
                        .font(.system(size: 11))
                        .foregroundColor(.red)
                } else if !preview.isEmpty {
                    previewList
                } else {
                    Text("每个 主机 × 端口 会创建一个通道，并分配一段连续的访问端口")
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                }
            }

            if !candidates.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text(isEdit ? "把已有通道加入该组（可选）" : "或加入已有通道（可选）")
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                    ScrollView {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(candidates) { t in
                                Toggle(isOn: Binding(
                                    get: { moveIDs.contains(t.id) },
                                    set: { on in if on { moveIDs.insert(t.id) } else { moveIDs.remove(t.id) } }
                                )) {
                                    HStack(spacing: 6) {
                                        Text(verbatim: t.name)
                                            .font(.system(size: 12))
                                            .lineLimit(1)
                                        Text(verbatim: ":\(t.listen_port) → \(t.target)")
                                            .font(.system(size: 11, design: .monospaced))
                                            .foregroundColor(.secondary)
                                            .lineLimit(1)
                                    }
                                }
                                .toggleStyle(.checkbox)
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: min(CGFloat(candidates.count) * 22 + 4, 110))
                }
            }

            if hasNewTargets {
                VStack(alignment: .leading, spacing: 6) {
                    Text("新建通道的设置")
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                    HStack(spacing: 6) {
                        Text("闲置关闭")
                            .font(.system(size: 11))
                            .foregroundColor(.secondary)
                            .frame(width: 56, alignment: .leading)
                        ForEach(idleCloseOptions, id: \.0) { opt in
                            DurationChip(title: opt.1, minutes: opt.0, selectedMinutes: $idleCloseMinutes)
                        }
                    }
                    HStack(spacing: 6) {
                        Text("最长开启")
                            .font(.system(size: 11))
                            .foregroundColor(.secondary)
                            .frame(width: 56, alignment: .leading)
                        DurationChip(title: "不限制", minutes: 0, selectedMinutes: $durationMinutes)
                        DurationChip(title: "2 小时", minutes: 120, selectedMinutes: $durationMinutes)
                        DurationChip(title: "8 小时", minutes: 480, selectedMinutes: $durationMinutes)
                        DurationChip(title: "1 天", minutes: 1440, selectedMinutes: $durationMinutes)
                    }
                    Picker("", selection: $accessMode) {
                        Text("仅授权链接（推荐）").tag(TunnelEditSheet.AccessMode.linkOnly)
                        Text("固定 IP / 网段").tag(TunnelEditSheet.AccessMode.specific)
                        Text("全部放通 *").tag(TunnelEditSheet.AccessMode.all)
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    if accessMode == .specific {
                        TextField("例如 192.168.1.100, 192.168.1.0/24（逗号分隔）", text: $whitelistString)
                            .textFieldStyle(.roundedBorder)
                    } else if accessMode == .all {
                        Label("任何能访问服务器的设备都可以连接，不建议用于敏感服务。", systemImage: "exclamationmark.triangle.fill")
                            .font(.system(size: 11))
                            .foregroundColor(.red)
                    }
                }
            }

            if let err = errorMessage {
                Text(verbatim: err)
                    .font(.system(size: 12))
                    .foregroundColor(.red)
            }

            HStack {
                Spacer()
                Button("取消") { isPresented = false }
                    .controlSize(.small)
                Button(isSaving ? "保存中…" : (isEdit ? "保存" : "创建通道组")) { save() }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
                    .disabled(!canSave)
            }
            .padding(.top, 2)
        }
        .padding(20)
        .frame(width: 560)
        .task(id: "\(name)|\(hosts)|\(ports)") {
            // 输入停顿后再预览，避免每敲一个字就请求一次
            try? await Task.sleep(nanoseconds: 300_000_000)
            guard !Task.isCancelled else { return }
            guard hasNewTargets else {
                preview = []
                previewError = nil
                return
            }
            let (members, err) = await vm.previewGroup(name: name.isEmpty ? "通道组" : name, hosts: hosts, ports: ports)
            guard !Task.isCancelled else { return }
            preview = members
            previewError = err
        }
    }

    var previewList: some View {
        let shown = preview.prefix(6)
        return VStack(alignment: .leading, spacing: 3) {
            ForEach(Array(shown)) { m in
                HStack(spacing: 6) {
                    Text(verbatim: "\(vm.publicHost):\(m.listen_port)")
                        .font(.system(size: 11, weight: .medium, design: .monospaced))
                        .foregroundColor(.accentColor)
                    Image(systemName: "arrow.right")
                        .font(.system(size: 8))
                        .foregroundColor(.secondary)
                    Text(verbatim: m.target)
                        .font(.system(size: 11, design: .monospaced))
                        .foregroundColor(.secondary)
                }
            }
            Text(verbatim: preview.count > shown.count ? "… 共 \(preview.count) 个地址" : "共 \(preview.count) 个地址")
                .font(.system(size: 11))
                .foregroundColor(.secondary)
        }
    }

    func field<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.system(size: 12))
                .foregroundColor(.secondary)
            content()
        }
    }

    func save() {
        var wl: [String] = []
        switch accessMode {
        case .linkOnly: wl = []
        case .all: wl = ["*"]
        case .specific:
            wl = whitelistString.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
            if hasNewTargets && wl.isEmpty {
                errorMessage = "请填写允许访问的 IP 或网段，或选择“仅授权链接”"
                return
            }
        }
        isSaving = true
        errorMessage = nil
        Task {
            let (gid, err) = await vm.saveGroup(
                id: group?.id, name: name.trimmingCharacters(in: .whitespaces), owner: owner.trimmingCharacters(in: .whitespaces),
                hosts: hasNewTargets ? hosts : "", ports: hasNewTargets ? ports : "", tunnelIds: Array(moveIDs),
                whitelist: wl, idleCloseMinutes: idleCloseMinutes, durationMinutes: durationMinutes, maxBandwidthMB: 0)
            isSaving = false
            if let err = err {
                errorMessage = err
                return
            }
            isPresented = false
            if !isEdit, accessMode != .all, let gid = gid {
                onCreated?(gid)
            }
        }
    }
}

struct InviteSheetTarget: Identifiable {
    let scope: AccessScope
    let autoGenerate: Bool
    var id: String { scope.key }

    init(scope: AccessScope, autoGenerate: Bool) {
        self.scope = scope
        self.autoGenerate = autoGenerate
    }

    init(tunnel: TunnelItem, autoGenerate: Bool) {
        self.init(scope: .tunnel(tunnel.id), autoGenerate: autoGenerate)
    }
}

/// 生成邀请链接并展示分享卡片（二维码 + 可直接发送的文案）
struct InviteSheet: View {
    @ObservedObject var vm: PortRelayViewModel
    let scope: AccessScope
    let autoGenerate: Bool
    @Binding var isPresented: Bool

    init(vm: PortRelayViewModel, scope: AccessScope, autoGenerate: Bool, isPresented: Binding<Bool>) {
        _vm = ObservedObject(wrappedValue: vm)
        self.scope = scope
        self.autoGenerate = autoGenerate
        _isPresented = isPresented
    }

    init(vm: PortRelayViewModel, tunnel: TunnelItem, autoGenerate: Bool, isPresented: Binding<Bool>) {
        self.init(vm: vm, scope: .tunnel(tunnel.id), autoGenerate: autoGenerate, isPresented: isPresented)
    }

    var title: String {
        let name = vm.scopeTitle(scope)
        return scope.isGroup ? "邀请访问「\(name)」（\(vm.scopeTunnels(scope).filter { $0.enabled }.count) 个地址）" : "邀请访问「\(name)」"
    }

    @State private var note: String = ""
    @State private var validMinutes: Int = 24 * 60
    @State private var maxUses: Int = 1
    @State private var leaseDuration: String = "2h"
    @State private var requireApproval: Bool = false
    @State private var result: InviteShareResp? = nil
    @State private var errorMessage: String? = nil
    @State private var isWorking = false
    @State private var copied: String? = nil

    let validOptions: [(Int, String)] = [(60, "1 小时"), (24 * 60, "24 小时"), (7 * 24 * 60, "7 天")]
    let useOptions: [(Int, String)] = [(1, "1 台"), (3, "3 台"), (0, "不限")]

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(verbatim: title)
                        .font(.system(size: 15, weight: .bold))
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Text(verbatim: result == nil
                         ? (scope.isGroup ? "同事授权一次即可访问组内全部地址" : "同事打开链接并填写姓名后获得访问权限")
                         : "授权后可访问 \(leaseDurationTitle(result?.invite.lease_duration))" + (result?.invite.require_approval == true ? "，需你审批" : "，无需审批"))
                        .font(.system(size: 12))
                        .foregroundColor(.secondary)
                }
                Spacer()
                Button("完成") { isPresented = false }
                    .buttonStyle(.borderedProminent)
                    .controlSize(.small)
            }

            if let r = result {
                HStack(alignment: .top, spacing: 16) {
                    if let qr = makeQRCode(r.link) {
                        VStack(spacing: 4) {
                            Image(nsImage: qr)
                                .interpolation(.none)
                                .resizable()
                                .frame(width: 150, height: 150)
                                .background(Color.white)
                                .cornerRadius(6)
                            Text("手机扫码打开")
                                .font(.system(size: 10))
                                .foregroundColor(.secondary)
                        }
                    }
                    VStack(alignment: .leading, spacing: 8) {
                        ScrollView {
                            Text(verbatim: r.share_text)
                                .font(.system(size: 12))
                                .textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .frame(height: scope.isGroup ? 170 : 130)
                        .padding(8)
                        .background(Color(NSColor.textBackgroundColor))
                        .cornerRadius(6)
                        .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Color(NSColor.separatorColor), lineWidth: 1))
                        HStack {
                            Button(copied == "text" ? "已复制" : "复制分享文案") {
                                copyToPasteboard(r.share_text)
                                copied = "text"
                            }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.small)
                            Button(copied == "link" ? "已复制" : "仅复制链接") {
                                copyToPasteboard(r.link)
                                copied = "link"
                            }
                            .controlSize(.small)
                            Spacer()
                            Button("再生成一个") {
                                result = nil
                                copied = nil
                            }
                            .buttonStyle(.link)
                            .font(.system(size: 11))
                        }
                    }
                }
            } else {
                VStack(alignment: .leading, spacing: 10) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("备注（发给谁，可选）")
                            .font(.system(size: 12))
                            .foregroundColor(.secondary)
                        TextField("例如：张三", text: $note)
                            .textFieldStyle(.roundedBorder)
                    }
                    HStack(spacing: 6) {
                        Text("链接有效期")
                            .font(.system(size: 12))
                            .foregroundColor(.secondary)
                            .frame(width: 84, alignment: .leading)
                        ForEach(validOptions, id: \.0) { opt in
                            DurationChip(title: opt.1, minutes: opt.0, selectedMinutes: $validMinutes)
                        }
                    }
                    HStack(spacing: 6) {
                        Text("可用设备数")
                            .font(.system(size: 12))
                            .foregroundColor(.secondary)
                            .frame(width: 84, alignment: .leading)
                        ForEach(useOptions, id: \.0) { opt in
                            DurationChip(title: opt.1, minutes: opt.0, selectedMinutes: $maxUses)
                        }
                    }
                    HStack(spacing: 6) {
                        Text("授权后可访问")
                            .font(.system(size: 12))
                            .foregroundColor(.secondary)
                            .frame(width: 84, alignment: .leading)
                        ForEach(leaseDurationOptions, id: \.value) { opt in
                            StringChip(title: opt.title, value: opt.value, selection: $leaseDuration)
                        }
                    }
                    HStack(spacing: 6) {
                        Text("审批")
                            .font(.system(size: 12))
                            .foregroundColor(.secondary)
                            .frame(width: 84, alignment: .leading)
                        Toggle("同事打开链接后需要我批准才放通", isOn: $requireApproval)
                            .font(.system(size: 12))
                    }
                    Text("“链接有效期”是同事必须在多久内打开链接；“授权后可访问”是打开并授权后能使用多久。链接用满设备数后自动失效，也可随时在“授权管理”中作废。")
                        .fixedSize(horizontal: false, vertical: true)
                        .font(.system(size: 11))
                        .foregroundColor(.secondary)
                    HStack {
                        Spacer()
                        Button(isWorking ? "生成中…" : "生成邀请链接") { generate() }
                            .buttonStyle(.borderedProminent)
                            .controlSize(.small)
                            .disabled(isWorking)
                    }
                }
            }

            if let err = errorMessage {
                Text(err)
                    .font(.system(size: 12))
                    .foregroundColor(.red)
            }
        }
        .padding(20)
        .frame(width: 560)
        .onAppear {
            if autoGenerate { generate() }
        }
    }

    func generate() {
        isWorking = true
        errorMessage = nil
        Task {
            let (resp, err) = await vm.createInvite(scope: scope, note: note.trimmingCharacters(in: .whitespaces),
                                                    validMinutes: validMinutes, maxUses: maxUses, leaseDuration: leaseDuration,
                                                    requireApproval: requireApproval)
            result = resp
            errorMessage = err
            isWorking = false
        }
    }
}

struct DurationChip: View {
    let title: String
    let minutes: Int
    @Binding var selectedMinutes: Int

    var isSelected: Bool { selectedMinutes == minutes }

    var body: some View {
        Button(action: { selectedMinutes = minutes }) {
            ChipLabel(title: title, selected: isSelected)
        }
        .buttonStyle(.borderless)
    }
}

struct BandwidthChip: View {
    let title: String
    let mb: Double
    @Binding var selectedMB: Double

    var isSelected: Bool { abs(selectedMB - mb) < 0.01 }

    var body: some View {
        Button(action: { selectedMB = mb }) {
            ChipLabel(title: title, selected: isSelected, tint: .purple)
        }
        .buttonStyle(.borderless)
    }
}

// MARK: - Settings Sheet

struct SettingsSheet: View {
    @ObservedObject var vm: PortRelayViewModel
    @Binding var isPresented: Bool

    @State private var serverAddr: String = ""
    @State private var token: String = ""
    @State private var publicHost: String = ""
    @State private var errorMessage: String? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("设置")
                .font(.system(size: 15, weight: .bold))

            VStack(alignment: .leading, spacing: 4) {
                Text("服务器控制地址 (地址:端口)")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                TextField("例如 gw.example.com:33000", text: $serverAddr)
                    .textFieldStyle(.roundedBorder)
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("安全令牌")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                SecureField("与服务器一致的令牌", text: $token)
                    .textFieldStyle(.roundedBorder)
            }

            VStack(alignment: .leading, spacing: 4) {
                Text("对外访问地址 (访问者连接通道时使用)")
                    .font(.system(size: 12))
                    .foregroundColor(.secondary)
                TextField("留空则使用服务器地址", text: $publicHost)
                    .textFieldStyle(.roundedBorder)
            }

            if let err = errorMessage {
                Text(err)
                    .font(.system(size: 12))
                    .foregroundColor(.red)
            }

            HStack {
                Spacer()
                Button("取消") { isPresented = false }
                    .controlSize(.small)
                Button("保存并重连") {
                    Task {
                        if let err = await vm.saveSettings(serverAddr: serverAddr, token: token, publicHost: publicHost) {
                            errorMessage = err
                        } else {
                            isPresented = false
                        }
                    }
                }
                .buttonStyle(.borderedProminent)
                .controlSize(.small)
            }
            .padding(.top, 6)
        }
        .padding(20)
        .frame(width: 400)
        .onAppear {
            Task {
                let (addr, t, host) = await vm.fetchSettings()
                serverAddr = addr
                token = t
                publicHost = host
            }
        }
    }
}

// MARK: - App Delegate & Menu Bar Extra

@MainActor
class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    var statusItem: NSStatusItem?
    private var mainWindow: NSWindow?

    func applicationDidFinishLaunching(_ notification: Notification) {
        PortRelayViewModel.shared.appDelegate = self
        setupStatusItem()

        // Hook window close button so it hides to menu bar instead of quitting
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) { [weak self] in
            if let window = NSApp.windows.first(where: { $0.title == "PortRelay" || $0.canBecomeMain }) {
                self?.mainWindow = window
                window.delegate = self
            }
        }
    }

    /// 关闭窗口：隐藏到菜单栏，同时从 Dock 移除图标
    func windowShouldClose(_ sender: NSWindow) -> Bool {
        sender.orderOut(nil)
        NSApp.setActivationPolicy(.accessory)
        return false
    }

    /// 彻底退出：先结束后台引擎再退出 App
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        Task { @MainActor in
            await PortRelayViewModel.shared.shutdownAgent()
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            showMainWindow()
        }
        return true
    }

    private func setupStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem?.button {
            button.image = NSImage(systemSymbolName: "arrow.left.arrow.right.circle", accessibilityDescription: "PortRelay")
            button.imagePosition = .imageLeading
            button.title = " 0"
        }
        rebuildMenu()
    }

    func updateStatusItemTitle(activeCount: Int) {
        guard let button = self.statusItem?.button else { return }
        let isConnected = PortRelayViewModel.shared.isConnected
        let pendingCount = PortRelayViewModel.shared.pendingRequests.count
        var iconName = isConnected ? (activeCount > 0 ? "arrow.left.arrow.right.circle.fill" : "arrow.left.arrow.right.circle") : "exclamationmark.triangle"
        if pendingCount > 0 { iconName = "bell.badge.fill" }
        button.image = NSImage(systemSymbolName: iconName, accessibilityDescription: "PortRelay")
        button.title = pendingCount > 0 ? " 待审批 \(pendingCount)" : " \(activeCount)"
        if PortRelayViewModel.shared.menuNeedsRebuild() {
            self.rebuildMenu()
        }
    }

    func rebuildMenu() {
        let menu = NSMenu()
        let vm = PortRelayViewModel.shared

        // Status Title
        let header = NSMenuItem(title: "PortRelay 端口中继", action: nil, keyEquivalent: "")
        header.isEnabled = false
        menu.addItem(header)

        let connStatus = vm.isConnected ? "🟢 服务器连接正常" : "🔴 服务器未连接 / 重试中"
        let statusItem = NSMenuItem(title: connStatus, action: nil, keyEquivalent: "")
        statusItem.isEnabled = false
        menu.addItem(statusItem)

        menu.addItem(NSMenuItem.separator())

        // Pending approvals
        let pending = vm.pendingRequests
        if !pending.isEmpty {
            let title = NSMenuItem(title: "待审批的访问申请 (\(pending.count))：", action: nil, keyEquivalent: "")
            title.isEnabled = false
            menu.addItem(title)
            for (title, scope, r) in pending {
                let item = NSMenuItem(title: "\(r.name)（\(r.ip)）→ \(title)", action: nil, keyEquivalent: "")
                let sub = NSMenu()
                let approve = NSMenuItem(title: "批准", action: #selector(decideAction(_:)), keyEquivalent: "")
                approve.representedObject = [scope.key, r.id, "1"]
                approve.target = self
                let reject = NSMenuItem(title: "拒绝", action: #selector(decideAction(_:)), keyEquivalent: "")
                reject.representedObject = [scope.key, r.id, "0"]
                reject.target = self
                sub.addItem(approve)
                sub.addItem(reject)
                item.submenu = sub
                menu.addItem(item)
            }
            menu.addItem(NSMenuItem.separator())
        }

        // Tunnels section
        if vm.tunnels.isEmpty {
            let emptyItem = NSMenuItem(title: "暂无通道", action: nil, keyEquivalent: "")
            emptyItem.isEnabled = false
            menu.addItem(emptyItem)
        } else {
            let tunnelSection = NSMenuItem(title: "中继通道列表 (\(vm.tunnels.filter { $0.enabled }.count)/\(vm.tunnels.count) 开启):", action: nil, keyEquivalent: "")
            tunnelSection.isEnabled = false
            menu.addItem(tunnelSection)

            for t in vm.tunnels {
                let check = t.enabled ? "✓ " : "   "
                let title = "\(check)\(t.name) (:\(t.listen_port))"
                let item = NSMenuItem(title: title, action: #selector(toggleTunnelAction(_:)), keyEquivalent: "")
                item.representedObject = t.id
                item.target = self
                menu.addItem(item)
            }
        }

        menu.addItem(NSMenuItem.separator())

        // Quick Actions
        let showItem = NSMenuItem(title: "打开主控制台", action: #selector(showMainWindowAction), keyEquivalent: "o")
        showItem.target = self
        menu.addItem(showItem)

        let reconnectItem = NSMenuItem(title: "立即重连网关", action: #selector(reconnectAction), keyEquivalent: "r")
        reconnectItem.target = self
        menu.addItem(reconnectItem)

        menu.addItem(NSMenuItem.separator())

        let quitItem = NSMenuItem(title: "彻底退出 PortRelay (含后台引擎)", action: #selector(quitAction), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)

        self.statusItem?.menu = menu
    }

    @objc func toggleTunnelAction(_ sender: NSMenuItem) {
        guard let id = sender.representedObject as? String else { return }
        Task {
            await PortRelayViewModel.shared.toggleTunnel(id: id)
        }
    }

    @objc func decideAction(_ sender: NSMenuItem) {
        guard let v = sender.representedObject as? [String], v.count == 3 else { return }
        let id = String(v[0].dropFirst(2))
        let scope: AccessScope = v[0].hasPrefix("g:") ? .group(id) : .tunnel(id)
        Task {
            _ = await PortRelayViewModel.shared.decideRequest(scope: scope, requestId: v[1], approve: v[2] == "1")
        }
    }

    @objc func showMainWindowAction() {
        showMainWindow()
    }

    @objc func reconnectAction() {
        Task {
            await PortRelayViewModel.shared.triggerReconnect(force: true)
        }
    }

    @objc func quitAction() {
        NSApp.terminate(nil)
    }

    func showMainWindow() {
        // 打开主窗口时恢复 Dock 图标
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        if let window = mainWindow {
            window.makeKeyAndOrderFront(nil)
        } else if let window = NSApp.windows.first(where: { $0.title == "PortRelay" || $0.canBecomeMain }) {
            mainWindow = window
            window.delegate = self
            window.makeKeyAndOrderFront(nil)
        }
    }
}

// MARK: - App Main

@main
struct PortRelayApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        WindowGroup("PortRelay") {
            ContentView()
        }
        .windowStyle(.titleBar)
        .defaultSize(width: 920, height: 640)
    }
}


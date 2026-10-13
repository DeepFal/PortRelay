package protocol

import "time"

// Message types
const (
	TypeAuth           = "auth"
	TypeAuthResp       = "auth_resp"
	TypeRegisterTunnel = "register_tunnel"
	TypeRegisterResp   = "register_resp"
	TypeUnregister     = "unregister_tunnel"
	TypeUnregisterResp = "unregister_resp"
	TypeUpdateTunnel   = "update_tunnel"
	TypeKillConn       = "kill_connection"
	TypeRevokeLease    = "revoke_lease"
	TypeCreateInvite   = "create_invite"
	TypeInviteResp     = "invite_resp"
	TypeRevokeInvite   = "revoke_invite"
	TypeDecideRequest  = "decide_request"
	TypeDeleteGroup    = "delete_group"
	TypeMetrics        = "metrics_update"
	TypePing           = "ping"
	TypePong           = "pong"
)

// BaseMessage represents any control message
type BaseMessage struct {
	Type string `json:"type"`
}

// AuthMessage sent by agent to authenticate
type AuthMessage struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

// AuthRespMessage returned by server
type AuthRespMessage struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// TunnelConfig is the definition of a tunnel
type TunnelConfig struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Target          string   `json:"target"` // Target host:port (accessed by Mac agent, e.g. 10.0.1.16:3306)
	ListenPort      int      `json:"listen_port"`
	Whitelist       []string `json:"whitelist"` // e.g. ["192.168.1.0/24", "192.168.1.100", "*"]
	Enabled         bool     `json:"enabled"`
	Owner           string   `json:"owner,omitempty"`            // 责任人
	DurationMinutes int      `json:"duration_minutes,omitempty"` // 最长开启时长，到时无论是否在用都关闭 (0: 不限制)
	// IdleCloseMinutes 闲置多久自动关闭（无在线连接、无有效授权、无待审批、无可用邀请）；nil 表示默认 30 分钟，0 表示不自动关闭
	IdleCloseMinutes *int `json:"idle_close_minutes,omitempty"`
	// 以下由程序维护
	CloseReason        string     `json:"close_reason,omitempty"`         // 上次关闭原因
	FirstConnectedAt   *time.Time `json:"first_connected_at,omitempty"`   // 本次开启后首次有连接的时间
	ConnectedSec       int64      `json:"connected_sec,omitempty"`        // 本次开启后有连接在线的累计秒数
	LastConnectedSec   int64      `json:"last_connected_sec,omitempty"`   // 上次开启期间有连接在线的累计秒数
	StartedAt          *time.Time `json:"started_at,omitempty"`           // 本次开启时间
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`           // 到期自动结束时间
	HistoricalBytesIn  int64      `json:"historical_bytes_in,omitempty"`  // 历史累计入流量
	HistoricalBytesOut int64      `json:"historical_bytes_out,omitempty"` // 历史累计出流量
	LastDurationSec    int64      `json:"last_duration_sec,omitempty"`    // 上次开启累计运行秒数
	MaxBandwidthMB     float64    `json:"max_bandwidth_mb,omitempty"`     // 最大带宽限速 (MB/s, 0 为不限制)
	AuthCode           string     `json:"auth_code,omitempty"`            // 网页放通授权识别码
	DynamicWhitelist   []string   `json:"dynamic_whitelist,omitempty"`    // 动态放通中的客户端 IP 列表
	Notes              string     `json:"notes,omitempty"`
	LeaseDuration      string     `json:"lease_duration,omitempty"`   // 已废弃：放通时长改为在每个邀请上设置，保留字段仅为兼容旧配置
	RequireApproval    bool       `json:"require_approval,omitempty"` // 已废弃：是否审批改为在每个邀请上设置，保留字段仅为兼容旧配置
	// GroupID 所属通道组：组内通道共用邀请、授权与审批，闲置计时联动
	GroupID string `json:"group_id,omitempty"`
	// GroupName 由 Agent 同步到服务端时填写，用于授权页展示（不持久化）
	GroupName string `json:"group_name,omitempty"`
}

// RegisterTunnelMessage registers a tunnel on server
type RegisterTunnelMessage struct {
	Type   string       `json:"type"`
	Tunnel TunnelConfig `json:"tunnel"`
}

// RegisterRespMessage response to register
type RegisterRespMessage struct {
	Type     string `json:"type"`
	TunnelID string `json:"tunnel_id"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	Port     int    `json:"port"`
}

// UnregisterMessage removes a tunnel
type UnregisterMessage struct {
	Type     string `json:"type"`
	TunnelID string `json:"tunnel_id"`
}

// UnregisterRespMessage response to unregister
type UnregisterRespMessage struct {
	Type     string `json:"type"`
	TunnelID string `json:"tunnel_id"`
	Success  bool   `json:"success"`
}

// UpdateTunnelMessage updates tunnel configuration (e.g. whitelist or name)
type UpdateTunnelMessage struct {
	Type   string       `json:"type"`
	Tunnel TunnelConfig `json:"tunnel"`
}

// KillConnMessage requests server to terminate a specific client connection
type KillConnMessage struct {
	Type   string `json:"type"`
	ConnID string `json:"conn_id"`
}

// RevokeLeaseMessage removes a temporary (web-auth) whitelist lease; empty IP revokes all leases of the scope.
// 设置 GroupID 时作用于整个通道组的授权（TunnelID 忽略），下同。
type RevokeLeaseMessage struct {
	Type     string `json:"type"`
	TunnelID string `json:"tunnel_id"`
	GroupID  string `json:"group_id,omitempty"`
	IP       string `json:"ip,omitempty"`
}

// DeleteGroupMessage drops the invites, leases and requests of a dissolved tunnel group
type DeleteGroupMessage struct {
	Type    string `json:"type"`
	GroupID string `json:"group_id"`
}

// Lease durations (Invite.LeaseDuration)：通过邀请获得授权后可访问多久
const (
	LeaseDuration1h     = "1h"
	LeaseDuration2h     = "2h"
	LeaseDuration8h     = "8h"
	LeaseDurationToday  = "today"  // 当天 23:59:59 前有效
	LeaseDurationTunnel = "tunnel" // 通道开启期间有效
)

// ValidLeaseDuration reports whether d is a supported lease duration ("" means default 2h)
func ValidLeaseDuration(d string) bool {
	switch d {
	case "", LeaseDuration1h, LeaseDuration2h, LeaseDuration8h, LeaseDurationToday, LeaseDurationTunnel:
		return true
	}
	return false
}

// DescribeLeaseDuration is the human-readable lease duration
func DescribeLeaseDuration(d string) string {
	switch d {
	case LeaseDuration1h:
		return "1 小时"
	case LeaseDuration8h:
		return "8 小时"
	case LeaseDurationToday:
		return "当天有效"
	case LeaseDurationTunnel:
		return "通道开启期间有效"
	default:
		return "2 小时"
	}
}

// DynamicLease is a temporary whitelist entry granted through an invite link
type DynamicLease struct {
	IP        string    `json:"ip"`
	ExpiresAt time.Time `json:"expires_at"`
	Name      string    `json:"name,omitempty"`   // 申请人填写的名字
	Invite    string    `json:"invite,omitempty"` // 来源邀请码
	// Duration 授权时长；计时从该设备首次连接开始
	Duration string `json:"duration,omitempty"`
	// AwaitingConnect 为 true 表示已授权但尚未首次连接：计时未开始，ExpiresAt 是必须首次连接的截止时间
	AwaitingConnect bool       `json:"awaiting_connect,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"` // 首次连接（开始计时）的时间
}

// InviteUse records one device that used an invite
type InviteUse struct {
	Name string    `json:"name"`
	IP   string    `json:"ip"`
	At   time.Time `json:"at"`
}

// Invite is a shareable link that lets a limited number of devices request access
type Invite struct {
	Code      string    `json:"code"`
	Note      string    `json:"note,omitempty"` // 管理员备注（发给谁）
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"` // 链接可被打开的截止时间
	MaxUses   int       `json:"max_uses"`   // 0 表示不限设备数
	// LeaseDuration 通过该邀请获得授权后可访问多久（每个邀请单独设置）
	LeaseDuration string `json:"lease_duration,omitempty"`
	// RequireApproval 通过该邀请提交的申请需管理员批准后才放通
	RequireApproval bool        `json:"require_approval,omitempty"`
	Uses            []InviteUse `json:"uses,omitempty"`
}

// AccessRequest is an access application on a tunnel (or tunnel group) that requires approval
type AccessRequest struct {
	ID       string    `json:"id"`
	TunnelID string    `json:"tunnel_id"`
	GroupID  string    `json:"group_id,omitempty"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	Invite   string    `json:"invite,omitempty"`
	At       time.Time `json:"at"`
	Status   string    `json:"status"` // pending / approved / rejected
}

// CreateInviteMessage asks the server to create an invite link
type CreateInviteMessage struct {
	Type         string `json:"type"`
	RequestID    string `json:"request_id"`
	TunnelID     string `json:"tunnel_id"`
	GroupID      string `json:"group_id,omitempty"`
	Note         string `json:"note,omitempty"`
	ValidMinutes int    `json:"valid_minutes"`
	MaxUses      int    `json:"max_uses"`
	// LeaseDuration 授权后可访问时长，见 LeaseDuration* 常量
	LeaseDuration string `json:"lease_duration,omitempty"`
	// RequireApproval 打开链接后需管理员批准才放通
	RequireApproval bool `json:"require_approval,omitempty"`
}

// InviteRespMessage answers CreateInviteMessage
type InviteRespMessage struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	Invite    Invite `json:"invite"`
}

// RevokeInviteMessage invalidates an invite and the leases it granted
type RevokeInviteMessage struct {
	Type     string `json:"type"`
	TunnelID string `json:"tunnel_id"`
	GroupID  string `json:"group_id,omitempty"`
	Code     string `json:"code"`
}

// DecideRequestMessage approves or rejects a pending access request
type DecideRequestMessage struct {
	Type      string `json:"type"`
	TunnelID  string `json:"tunnel_id"`
	GroupID   string `json:"group_id,omitempty"`
	RequestID string `json:"request_id"`
	Approve   bool   `json:"approve"`
}

// ActiveConnInfo details about currently connected client
type ActiveConnInfo struct {
	ConnID      string    `json:"conn_id"`
	TunnelID    string    `json:"tunnel_id"`
	ClientIP    string    `json:"client_ip"`
	ConnectedAt time.Time `json:"connected_at"`
	BytesIn     int64     `json:"bytes_in"`  // Upload from client to target
	BytesOut    int64     `json:"bytes_out"` // Download from target to client
	RateIn      int64     `json:"rate_in"`   // Current bytes/sec
	RateOut     int64     `json:"rate_out"`  // Current bytes/sec
}

// ClientHistoryRecord historical record of a client connection
type ClientHistoryRecord struct {
	ID             string    `json:"id"`
	TunnelID       string    `json:"tunnel_id"`
	TunnelName     string    `json:"tunnel_name"`
	Port           int       `json:"port"`
	ClientIP       string    `json:"client_ip"`
	ConnectedAt    time.Time `json:"connected_at"`
	DisconnectedAt time.Time `json:"disconnected_at"`
	DurationSec    int64     `json:"duration_sec"`
	BytesIn        int64     `json:"bytes_in"`
	BytesOut       int64     `json:"bytes_out"`
}

// TunnelMetrics summary statistics for a tunnel
type TunnelMetrics struct {
	TunnelID         string          `json:"tunnel_id"`
	ListenPort       int             `json:"listen_port"`
	ActiveConns      int             `json:"active_conns"`
	TotalConns       int64           `json:"total_conns"`
	BytesIn          int64           `json:"bytes_in"`  // 本次会话入流量
	BytesOut         int64           `json:"bytes_out"` // 本次会话出流量
	RateIn           int64           `json:"rate_in"`
	RateOut          int64           `json:"rate_out"`
	BlockedAttemps   int64           `json:"blocked_attempts"`
	Status           string          `json:"status"` // "online", "error", "disabled"
	Error            string          `json:"error,omitempty"`
	DynamicWhitelist []string        `json:"dynamic_whitelist,omitempty"`
	DynamicLeases    []DynamicLease  `json:"dynamic_leases,omitempty"`
	Invites          []Invite        `json:"invites,omitempty"`
	Pending          []AccessRequest `json:"pending,omitempty"`
}

// GroupMetrics carries the access state shared by all tunnels of a group
type GroupMetrics struct {
	GroupID       string          `json:"group_id"`
	DynamicLeases []DynamicLease  `json:"dynamic_leases,omitempty"`
	Invites       []Invite        `json:"invites,omitempty"`
	Pending       []AccessRequest `json:"pending,omitempty"`
}

// SecurityEvent records blocked or flagged connection attempts
type SecurityEvent struct {
	ID        string    `json:"id"`
	TunnelID  string    `json:"tunnel_id"`
	Port      int       `json:"port"`
	ClientIP  string    `json:"client_ip"`
	Timestamp time.Time `json:"timestamp"`
	Reason    string    `json:"reason"`
}

// MetricsMessage pushed by server to agent periodically (e.g. every 1s)
type MetricsMessage struct {
	Type           string           `json:"type"`
	Tunnels        []TunnelMetrics  `json:"tunnels"`
	Groups         []GroupMetrics   `json:"groups,omitempty"`
	ActiveConns    []ActiveConnInfo `json:"active_conns"`
	SecurityEvents []SecurityEvent  `json:"security_events,omitempty"`
}

// StreamHandshake sent as first line when a data stream is opened
type StreamHandshake struct {
	TunnelID string `json:"tunnel_id"`
	ConnID   string `json:"conn_id"`
	ClientIP string `json:"client_ip"`
}

package client

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DeepFal/PortRelay/core/acl"
	"github.com/DeepFal/PortRelay/core/protocol"
)

const (
	PortMin = 33000
	PortMax = 34000
	// ControlPort 是中继服务器上 PortRelay 自身的控制端口，不能分配给通道
	ControlPort = 33000

	// DefaultIdleCloseMinutes 未设置时的闲置自动关闭时长
	DefaultIdleCloseMinutes = 30

	maxClientHistory = 200
	// 连接历史的落盘延迟：短连接密集时合并写入，避免每断开一个连接就重写一次配置
	historySaveDelay = 2 * time.Second
)

func generateAuthCode() string {
	const chars = "23456789abcdefghjkmnpqrstuvwxyz"
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			panic(err)
		}
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

// TunnelGroup 通道组：组内通道共用邀请、授权与审批，闲置计时联动
type TunnelGroup struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Config represents persistent client settings
type Config struct {
	ServerAddr    string                         `json:"server_addr"` // 中继服务器控制地址 (host:port)
	Token         string                         `json:"token"`
	PublicHost    string                         `json:"public_host,omitempty"` // 访问者连接通道时使用的网关地址
	Tunnels       []protocol.TunnelConfig        `json:"tunnels"`
	Groups        []TunnelGroup                  `json:"groups,omitempty"`
	ClientHistory []protocol.ClientHistoryRecord `json:"client_history,omitempty"`
}

// ConfigManager handles loading and saving configuration
type ConfigManager struct {
	path string
	mu   sync.RWMutex
	cfg  Config

	historyTimer *time.Timer
	closed       bool
}

// DefaultConfig returns the first-run config: no server and no tunnels.
// 服务器地址、令牌与通道都属于用户数据，由用户在界面中填写，不能写死在程序里。
func DefaultConfig() Config {
	return Config{
		Tunnels:       make([]protocol.TunnelConfig, 0),
		ClientHistory: make([]protocol.ClientHistoryRecord, 0),
	}
}

// NewConfigManager initializes or loads config from ~/.portrelay/config.json
func NewConfigManager() (*ConfigManager, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return NewConfigManagerAt(filepath.Join(home, ".portrelay", "config.json"))
}

// NewConfigManagerAt initializes or loads config from the given path
func NewConfigManagerAt(filePath string) (*ConfigManager, error) {
	if err := os.MkdirAll(filepath.Dir(filePath), 0700); err != nil {
		return nil, err
	}
	cm := &ConfigManager{path: filePath}
	err := cm.load()
	switch {
	case err == nil:
		return cm, nil
	case errors.Is(err, fs.ErrNotExist):
		cm.cfg = DefaultConfig()
	default:
		// 配置损坏时先备份原文件，绝不直接覆盖用户数据
		backup := fmt.Sprintf("%s.corrupt-%s", filePath, time.Now().Format("20060102-150405"))
		if rerr := os.Rename(filePath, backup); rerr != nil {
			return nil, fmt.Errorf("config %s is unreadable (%v) and could not be backed up: %w", filePath, err, rerr)
		}
		log.Printf("[Config] 配置文件损坏 (%v)，已备份到 %s 并使用默认配置", err, backup)
		cm.cfg = DefaultConfig()
	}
	if err := cm.save(); err != nil {
		return nil, err
	}
	return cm, nil
}

func (cm *ConfigManager) load() error {
	data, err := os.ReadFile(cm.path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	// Ensure every tunnel has an auth code
	for i := range cfg.Tunnels {
		if cfg.Tunnels[i].AuthCode == "" {
			cfg.Tunnels[i].AuthCode = generateAuthCode()
		}
	}
	cm.cfg = cfg
	return nil
}

// save writes the config atomically (temp file + rename). Caller must hold cm.mu.
func (cm *ConfigManager) save() error {
	if cm.closed {
		return nil
	}
	data, err := json.MarshalIndent(cm.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cm.path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	return os.Rename(tmpName, cm.path)
}

// Close persists pending writes; afterwards the manager no longer touches the file (used on shutdown)
func (cm *ConfigManager) Close() error {
	err := cm.Flush()
	cm.mu.Lock()
	cm.closed = true
	cm.mu.Unlock()
	return err
}

// Flush persists any pending (debounced) writes
func (cm *ConfigManager) Flush() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.historyTimer != nil {
		cm.historyTimer.Stop()
		cm.historyTimer = nil
	}
	return cm.save()
}

func cloneTunnel(t protocol.TunnelConfig) protocol.TunnelConfig {
	// 空白名单（仅授权链接）必须序列化为 []，不能是 null：App 端按非可选数组解析
	t.Whitelist = append(make([]string, 0, len(t.Whitelist)), t.Whitelist...)
	t.DynamicWhitelist = append([]string(nil), t.DynamicWhitelist...)
	if t.StartedAt != nil {
		v := *t.StartedAt
		t.StartedAt = &v
	}
	if t.ExpiresAt != nil {
		v := *t.ExpiresAt
		t.ExpiresAt = &v
	}
	return t
}

// Get returns a deep copy snapshot (safe to read without holding the lock)
func (cm *ConfigManager) Get() Config {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	out := cm.cfg
	out.Tunnels = make([]protocol.TunnelConfig, len(cm.cfg.Tunnels))
	for i, t := range cm.cfg.Tunnels {
		out.Tunnels[i] = cloneTunnel(t)
	}
	out.Groups = append([]TunnelGroup(nil), cm.cfg.Groups...)
	out.ClientHistory = append([]protocol.ClientHistoryRecord(nil), cm.cfg.ClientHistory...)
	return out
}

// FindTunnel returns a copy of the tunnel with the given ID
func (cm *ConfigManager) FindTunnel(id string) (protocol.TunnelConfig, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for _, t := range cm.cfg.Tunnels {
		if t.ID == id {
			return cloneTunnel(t), true
		}
	}
	return protocol.TunnelConfig{}, false
}

// ValidateServerAddr checks a host:port address
func ValidateServerAddr(addr string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || host == "" {
		return fmt.Errorf("服务器地址格式应为 IP:端口")
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("服务器端口无效")
	}
	return nil
}

// PublicHostOrDefault returns the host LAN users connect to;
// 未单独配置时使用服务器控制地址中的主机部分
func (c Config) PublicHostOrDefault() string {
	if h := strings.TrimSpace(c.PublicHost); h != "" {
		return h
	}
	if h, _, err := net.SplitHostPort(c.ServerAddr); err == nil {
		return h
	}
	return ""
}

// UpdateServer sets server address, token and the public access host.
// 返回 true 表示连接参数（地址/令牌）发生变化，需要重连。
func (cm *ConfigManager) UpdateServer(addr, token, publicHost string) (bool, error) {
	addr = strings.TrimSpace(addr)
	token = strings.TrimSpace(token)
	publicHost = strings.TrimSpace(publicHost)
	if err := ValidateServerAddr(addr); err != nil {
		return false, err
	}
	if publicHost != "" && (strings.ContainsAny(publicHost, " /:") || strings.Contains(publicHost, "://")) {
		return false, fmt.Errorf("对外访问地址只填主机名或 IP，不要带端口或协议")
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	changed := cm.cfg.ServerAddr != addr || cm.cfg.Token != token
	cm.cfg.ServerAddr = addr
	cm.cfg.Token = token
	cm.cfg.PublicHost = publicHost
	return changed, cm.save()
}

// normalizeAndValidate trims user input and checks the user-editable fields
func normalizeAndValidate(t *protocol.TunnelConfig) error {
	t.Name = strings.TrimSpace(t.Name)
	t.Target = strings.TrimSpace(t.Target)
	t.Owner = strings.TrimSpace(t.Owner)
	if t.Name == "" || t.Target == "" {
		return fmt.Errorf("名称与目标地址为必填项")
	}
	if host, port, err := net.SplitHostPort(t.Target); err != nil || host == "" {
		return fmt.Errorf("目标地址格式应为 主机:端口，例如 10.0.0.10:3306")
	} else if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("目标端口无效")
	}
	if t.ListenPort < PortMin || t.ListenPort > PortMax {
		return fmt.Errorf("监听端口须在 %d-%d 范围内", PortMin, PortMax)
	}
	if t.ListenPort == ControlPort {
		return fmt.Errorf("端口 %d 是服务器控制端口，不能用于通道", ControlPort)
	}
	if t.DurationMinutes < 0 {
		return fmt.Errorf("开启时长不能为负数")
	}
	if t.MaxBandwidthMB < 0 {
		return fmt.Errorf("限速不能为负数")
	}
	if t.IdleCloseMinutes != nil && (*t.IdleCloseMinutes < 0 || *t.IdleCloseMinutes > 7*24*60) {
		return fmt.Errorf("闲置自动关闭时长须在 0（不自动关闭）到 7 天之间")
	}

	// 白名单为空表示“仅授权链接”：只有通过邀请链接放通的设备可以访问。
	// 放通全部必须显式填写 "*"，不能因为留空而意外对所有人开放。
	wl := make([]string, 0, len(t.Whitelist))
	for _, r := range t.Whitelist {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if err := acl.ValidateRule(r); err != nil {
			return err
		}
		wl = append(wl, r)
	}
	t.Whitelist = wl

	// 放通时长与是否审批已改为在每个邀请上设置，通道级字段不再使用
	t.LeaseDuration = ""
	t.RequireApproval = false
	// 组名只在同步到服务端时填写，配置里以组 ID 为准
	t.GroupName = ""
	return nil
}

// groupExistsLocked reports whether a group ID is known ("" means no group). Caller must hold cm.mu.
func (cm *ConfigManager) groupExistsLocked(id string) bool {
	if id == "" {
		return true
	}
	for _, g := range cm.cfg.Groups {
		if g.ID == id {
			return true
		}
	}
	return false
}

// checkPortLocked returns an error if another tunnel already uses the port. Caller must hold cm.mu.
func (cm *ConfigManager) checkPortLocked(id string, port int) error {
	for _, existing := range cm.cfg.Tunnels {
		if existing.ID != id && existing.ListenPort == port {
			return fmt.Errorf("端口 %d 已被「%s」使用", port, existing.Name)
		}
	}
	return nil
}

// AddTunnel adds a new (enabled) tunnel
func (cm *ConfigManager) AddTunnel(t protocol.TunnelConfig) (protocol.TunnelConfig, error) {
	if err := normalizeAndValidate(&t); err != nil {
		return t, err
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := cm.checkPortLocked("", t.ListenPort); err != nil {
		return t, err
	}
	if !cm.groupExistsLocked(t.GroupID) {
		return t, fmt.Errorf("通道组不存在")
	}
	prepareNew(&t, time.Now())
	cm.cfg.Tunnels = append(cm.cfg.Tunnels, t)
	return cloneTunnel(t), cm.save()
}

// prepareNew resets the program-maintained fields of a tunnel being created and opens it
func prepareNew(t *protocol.TunnelConfig, now time.Time) {
	t.ID = uuid.New().String()
	t.AuthCode = generateAuthCode()
	t.HistoricalBytesIn, t.HistoricalBytesOut, t.LastDurationSec = 0, 0, 0
	t.CloseReason, t.FirstConnectedAt, t.ConnectedSec, t.LastConnectedSec = "", nil, 0, 0
	t.DynamicWhitelist = nil
	if t.IdleCloseMinutes == nil {
		v := DefaultIdleCloseMinutes
		t.IdleCloseMinutes = &v
	}
	t.Enabled = true
	start := now
	t.StartedAt = &start
	t.ExpiresAt = nil
	if t.DurationMinutes > 0 {
		exp := now.Add(time.Duration(t.DurationMinutes) * time.Minute)
		t.ExpiresAt = &exp
	}
}

// NewTunnels is one submission of the add-tunnel form
type NewTunnels struct {
	// Template 通道设置。Target 可以写多个目标（见 ExpandTargets）；ListenPort 只用于单个目标，
	// 多个目标自动分配一段连续端口；GroupID 表示加入已有通道组。
	Template protocol.TunnelConfig
	// NewGroup 新建一个通道组并把这些通道放进去
	NewGroup string
}

// CreateTunnels adds the tunnels of one add-tunnel form. Several targets become one tunnel each,
// named "名称 (主机)", and must go into a group; a group member without a name is named after the
// group. With dryRun nothing is saved and the result is a preview.
// Returns the group the tunnels joined (nil when ungrouped) and the created tunnels.
func (cm *ConfigManager) CreateTunnels(req NewTunnels, dryRun bool) (*TunnelGroup, []protocol.TunnelConfig, error) {
	tpl := req.Template
	targets, err := ExpandTargets(tpl.Target, "")
	if err != nil {
		return nil, nil, err
	}
	newGroup := strings.TrimSpace(req.NewGroup)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	var group *TunnelGroup
	switch {
	case newGroup != "":
		if err := cm.checkGroupNameLocked("", newGroup); err != nil {
			return nil, nil, err
		}
		group = &TunnelGroup{ID: uuid.New().String(), Name: newGroup, Owner: strings.TrimSpace(tpl.Owner), CreatedAt: time.Now()}
	case tpl.GroupID != "":
		g, ok := cm.findGroupLocked(tpl.GroupID)
		if !ok {
			return nil, nil, fmt.Errorf("通道组不存在")
		}
		group = &g
	case len(targets) > 1 && !dryRun:
		return nil, nil, fmt.Errorf("多个目标需要放进同一个通道组，请选择或新建通道组")
	}

	name := strings.TrimSpace(tpl.Name)
	base := name
	if base == "" && group != nil {
		base = group.Name
	}
	if base == "" && dryRun {
		base = "通道"
	}
	names := []string{base}
	if len(targets) > 1 || (name == "" && group != nil) {
		names = memberNames(base, targets)
	}

	var ports []int
	if len(targets) == 1 && tpl.ListenPort > 0 {
		if err := cm.checkPortLocked("", tpl.ListenPort); err != nil {
			return nil, nil, err
		}
		ports = []int{tpl.ListenPort}
	} else if ports, err = cm.allocatePortsLocked(len(targets)); err != nil {
		return nil, nil, err
	}

	now := time.Now()
	created := make([]protocol.TunnelConfig, 0, len(targets))
	for i, target := range targets {
		t := cloneTunnel(tpl)
		t.Name, t.Target, t.ListenPort, t.GroupID = names[i], target, ports[i], ""
		if group != nil {
			t.GroupID = group.ID
		}
		if err := normalizeAndValidate(&t); err != nil {
			if len(targets) > 1 {
				err = fmt.Errorf("%s：%w", target, err)
			}
			return nil, nil, err
		}
		prepareNew(&t, now)
		created = append(created, t)
	}
	if dryRun {
		return group, created, nil
	}
	if newGroup != "" {
		cm.cfg.Groups = append(cm.cfg.Groups, *group)
	}
	cm.cfg.Tunnels = append(cm.cfg.Tunnels, created...)
	out := make([]protocol.TunnelConfig, len(created))
	for i, t := range created {
		out[i] = cloneTunnel(t)
	}
	return group, out, cm.save()
}

// PatchTunnel applies a partial JSON update: only fields present in patch change.
// 启用状态、计时、流量统计、授权码由服务端逻辑维护，不能通过编辑修改。
// Returns the updated tunnel and its previous listen port.
func (cm *ConfigManager) PatchTunnel(id string, patch []byte) (protocol.TunnelConfig, int, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	idx := -1
	for i, t := range cm.cfg.Tunnels {
		if t.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return protocol.TunnelConfig{}, 0, fmt.Errorf("通道 %s 未找到", id)
	}

	existing := cm.cfg.Tunnels[idx]
	updated := cloneTunnel(existing)
	if err := json.Unmarshal(patch, &updated); err != nil {
		return existing, existing.ListenPort, fmt.Errorf("Invalid JSON")
	}
	// new_group：新建一个通道组并把该通道移进去
	var extra struct {
		NewGroup string `json:"new_group"`
	}
	_ = json.Unmarshal(patch, &extra)

	// 受保护字段
	updated.ID = existing.ID
	updated.Enabled = existing.Enabled
	updated.StartedAt = existing.StartedAt
	updated.ExpiresAt = existing.ExpiresAt
	updated.HistoricalBytesIn = existing.HistoricalBytesIn
	updated.HistoricalBytesOut = existing.HistoricalBytesOut
	updated.LastDurationSec = existing.LastDurationSec
	updated.CloseReason = existing.CloseReason
	updated.FirstConnectedAt = existing.FirstConnectedAt
	updated.ConnectedSec = existing.ConnectedSec
	updated.LastConnectedSec = existing.LastConnectedSec
	updated.AuthCode = existing.AuthCode
	updated.DynamicWhitelist = nil

	if err := normalizeAndValidate(&updated); err != nil {
		return existing, existing.ListenPort, err
	}
	if err := cm.checkPortLocked(id, updated.ListenPort); err != nil {
		return existing, existing.ListenPort, err
	}
	var newGroup *TunnelGroup
	if name := strings.TrimSpace(extra.NewGroup); name != "" {
		if err := cm.checkGroupNameLocked("", name); err != nil {
			return existing, existing.ListenPort, err
		}
		newGroup = &TunnelGroup{ID: uuid.New().String(), Name: name, Owner: updated.Owner, CreatedAt: time.Now()}
		updated.GroupID = newGroup.ID
	} else if !cm.groupExistsLocked(updated.GroupID) {
		return existing, existing.ListenPort, fmt.Errorf("通道组不存在")
	}

	// 运行中修改时长：从本次开启时间重新计算到期时间
	if updated.Enabled && updated.DurationMinutes != existing.DurationMinutes {
		updated.ExpiresAt = nil
		if updated.DurationMinutes > 0 {
			start := time.Now()
			if updated.StartedAt != nil {
				start = *updated.StartedAt
			}
			exp := start.Add(time.Duration(updated.DurationMinutes) * time.Minute)
			updated.ExpiresAt = &exp
		}
	}

	if newGroup != nil {
		cm.cfg.Groups = append(cm.cfg.Groups, *newGroup)
	}
	cm.cfg.Tunnels[idx] = updated
	return cloneTunnel(updated), existing.ListenPort, cm.save()
}

// ExtendUntil keeps an open tunnel open at least until t: an approved access extension outlasts the
// tunnel's maximum open time. Returns the tunnel and whether it changed.
func (cm *ConfigManager) ExtendUntil(id string, t time.Time) (protocol.TunnelConfig, bool, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for i, tc := range cm.cfg.Tunnels {
		if tc.ID != id {
			continue
		}
		if !tc.Enabled || tc.ExpiresAt == nil || !tc.ExpiresAt.Before(t) {
			return cloneTunnel(tc), false, nil
		}
		v := t
		cm.cfg.Tunnels[i].ExpiresAt = &v
		return cloneTunnel(cm.cfg.Tunnels[i]), true, cm.save()
	}
	return protocol.TunnelConfig{}, false, fmt.Errorf("通道 %s 未找到", id)
}

// RefreshAuthCode generates a new auth code for a tunnel
func (cm *ConfigManager) RefreshAuthCode(id string) (protocol.TunnelConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, t := range cm.cfg.Tunnels {
		if t.ID == id {
			t.AuthCode = generateAuthCode()
			cm.cfg.Tunnels[i] = t
			return cloneTunnel(t), cm.save()
		}
	}
	return protocol.TunnelConfig{}, fmt.Errorf("通道 %s 未找到", id)
}

// CurrentEnabled reports whether the tunnel is enabled
func (cm *ConfigManager) CurrentEnabled(id string) (bool, error) {
	t, ok := cm.FindTunnel(id)
	if !ok {
		return false, fmt.Errorf("未找到通道 %s", id)
	}
	return t.Enabled, nil
}

// IdleCloseMinutes returns the effective idle auto-close setting (0 = never)
func IdleCloseMinutes(t protocol.TunnelConfig) int {
	if t.IdleCloseMinutes == nil {
		return DefaultIdleCloseMinutes
	}
	return *t.IdleCloseMinutes
}

// stopLocked marks a tunnel stopped: records run time, real connected time, traffic and the reason
func stopLocked(t *protocol.TunnelConfig, now time.Time, sessionIn, sessionOut int64, reason string) {
	t.Enabled = false
	if t.StartedAt != nil {
		t.LastDurationSec = int64(now.Sub(*t.StartedAt).Seconds())
	}
	t.LastConnectedSec = t.ConnectedSec
	t.HistoricalBytesIn += sessionIn
	t.HistoricalBytesOut += sessionOut
	t.StartedAt = nil
	t.ExpiresAt = nil
	t.CloseReason = reason
}

// CloseTunnel stops an enabled tunnel with the given reason (e.g. idle auto-close)
func (cm *ConfigManager) CloseTunnel(id, reason string, sessionBytes func(id string) (int64, int64)) (protocol.TunnelConfig, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for i, t := range cm.cfg.Tunnels {
		if t.ID != id || !t.Enabled {
			continue
		}
		in, out := sessionBytes(t.ID)
		stopLocked(&t, time.Now(), in, out, reason)
		cm.cfg.Tunnels[i] = t
		if err := cm.save(); err != nil {
			log.Printf("[Config] save failed: %v", err)
		}
		return cloneTunnel(t), true
	}
	return protocol.TunnelConfig{}, false
}

// MarkFirstConnected records the first connection of the current session (once)
func (cm *ConfigManager) MarkFirstConnected(id string, at time.Time) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for i, t := range cm.cfg.Tunnels {
		if t.ID == id && t.Enabled && t.FirstConnectedAt == nil {
			cm.cfg.Tunnels[i].FirstConnectedAt = &at
			if err := cm.save(); err != nil {
				log.Printf("[Config] save failed: %v", err)
			}
			return
		}
	}
}

// AddConnectedSeconds accumulates the real connected time of the current session
func (cm *ConfigManager) AddConnectedSeconds(id string, secs int64) {
	if secs <= 0 {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for i, t := range cm.cfg.Tunnels {
		if t.ID == id && t.Enabled {
			cm.cfg.Tunnels[i].ConnectedSec += secs
			if err := cm.save(); err != nil {
				log.Printf("[Config] save failed: %v", err)
			}
			return
		}
	}
}

// SetTunnelEnabled turns a tunnel on/off, handling TTL and merging session traffic on stop.
// changed=false means it was already in the requested state (idempotent).
func (cm *ConfigManager) SetTunnelEnabled(id string, enabled bool, sessionIn, sessionOut int64) (protocol.TunnelConfig, bool, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for i, t := range cm.cfg.Tunnels {
		if t.ID != id {
			continue
		}
		if t.Enabled == enabled {
			return cloneTunnel(t), false, nil
		}
		now := time.Now()
		if enabled {
			t.Enabled = true
			t.StartedAt = &now
			t.ExpiresAt = nil
			if t.DurationMinutes > 0 {
				exp := now.Add(time.Duration(t.DurationMinutes) * time.Minute)
				t.ExpiresAt = &exp
			}
			t.CloseReason, t.FirstConnectedAt, t.ConnectedSec = "", nil, 0
		} else {
			stopLocked(&t, now, sessionIn, sessionOut, "手动关闭")
		}
		cm.cfg.Tunnels[i] = t
		return cloneTunnel(t), true, cm.save()
	}
	return protocol.TunnelConfig{}, false, fmt.Errorf("未找到通道 %s", id)
}

// ExpireDue disables every enabled tunnel whose TTL has passed and returns them.
// sessionBytes returns (and consumes) the current session traffic for a tunnel.
func (cm *ConfigManager) ExpireDue(sessionBytes func(id string) (int64, int64)) []protocol.TunnelConfig {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	now := time.Now()
	var expired []protocol.TunnelConfig
	for i, t := range cm.cfg.Tunnels {
		if !t.Enabled || t.ExpiresAt == nil || !now.After(*t.ExpiresAt) {
			continue
		}
		in, out := sessionBytes(t.ID)
		stopLocked(&t, now, in, out, fmt.Sprintf("达到最长开启时长（%s）", formatMinutes(t.DurationMinutes)))
		cm.cfg.Tunnels[i] = t
		expired = append(expired, cloneTunnel(t))
	}
	if len(expired) > 0 {
		if err := cm.save(); err != nil {
			log.Printf("[Config] save failed: %v", err)
		}
	}
	return expired
}

func formatMinutes(m int) string {
	switch {
	case m >= 60*24 && m%(60*24) == 0:
		return fmt.Sprintf("%d 天", m/(60*24))
	case m >= 60 && m%60 == 0:
		return fmt.Sprintf("%d 小时", m/60)
	default:
		return fmt.Sprintf("%d 分钟", m)
	}
}

// AddHistoricalTraffic merges session traffic that is about to be lost (disconnect / port move)
func (cm *ConfigManager) AddHistoricalTraffic(traffic map[string][2]int64) {
	if len(traffic) == 0 {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	changed := false
	for i, t := range cm.cfg.Tunnels {
		if v, ok := traffic[t.ID]; ok && (v[0] != 0 || v[1] != 0) {
			cm.cfg.Tunnels[i].HistoricalBytesIn += v[0]
			cm.cfg.Tunnels[i].HistoricalBytesOut += v[1]
			changed = true
		}
	}
	if changed {
		if err := cm.save(); err != nil {
			log.Printf("[Config] save failed: %v", err)
		}
	}
}

// DeleteTunnel removes a tunnel
func (cm *ConfigManager) DeleteTunnel(id string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	newTunnels := make([]protocol.TunnelConfig, 0, len(cm.cfg.Tunnels))
	found := false
	for _, t := range cm.cfg.Tunnels {
		if t.ID == id {
			found = true
			continue
		}
		newTunnels = append(newTunnels, t)
	}
	if !found {
		return fmt.Errorf("通道 %s 未找到", id)
	}
	cm.cfg.Tunnels = newTunnels
	return cm.save()
}

// AddClientHistory appends a client connection log record (persisted with a short delay)
func (cm *ConfigManager) AddClientHistory(rec protocol.ClientHistoryRecord) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	hist := make([]protocol.ClientHistoryRecord, 0, min(len(cm.cfg.ClientHistory)+1, maxClientHistory))
	hist = append(hist, rec)
	hist = append(hist, cm.cfg.ClientHistory...)
	if len(hist) > maxClientHistory {
		hist = hist[:maxClientHistory]
	}
	cm.cfg.ClientHistory = hist

	if cm.historyTimer == nil && !cm.closed {
		cm.historyTimer = time.AfterFunc(historySaveDelay, func() {
			cm.mu.Lock()
			defer cm.mu.Unlock()
			cm.historyTimer = nil
			if err := cm.save(); err != nil {
				log.Printf("[Config] save failed: %v", err)
			}
		})
	}
}

// GetClientHistory returns client connection records (optionally filtered by tunnelId)
func (cm *ConfigManager) GetClientHistory(tunnelID string) []protocol.ClientHistoryRecord {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	filtered := make([]protocol.ClientHistoryRecord, 0)
	for _, r := range cm.cfg.ClientHistory {
		if tunnelID == "" || r.TunnelID == tunnelID {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// RecommendPort finds an unused port in 33000-34000
func (cm *ConfigManager) RecommendPort() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	used := make(map[int]bool)
	for _, t := range cm.cfg.Tunnels {
		used[t.ListenPort] = true
	}

	for p := 33660; p <= PortMax; p++ {
		if !used[p] {
			return p
		}
	}
	for p := PortMin; p < 33660; p++ {
		if p == ControlPort {
			continue
		}
		if !used[p] {
			return p
		}
	}
	return 0
}

// ---- 通道组 ----

// FindGroup returns the group with the given ID
func (cm *ConfigManager) FindGroup(id string) (TunnelGroup, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.findGroupLocked(id)
}

func (cm *ConfigManager) findGroupLocked(id string) (TunnelGroup, bool) {
	for _, g := range cm.cfg.Groups {
		if g.ID == id {
			return g, true
		}
	}
	return TunnelGroup{}, false
}

// checkGroupNameLocked validates the name for group id ("" for a new group). Caller must hold cm.mu.
func (cm *ConfigManager) checkGroupNameLocked(id, name string) error {
	if name == "" {
		return fmt.Errorf("请填写通道组名称")
	}
	if len([]rune(name)) > 64 {
		return fmt.Errorf("通道组名称不能超过 64 个字")
	}
	for _, g := range cm.cfg.Groups {
		if g.ID != id && g.Name == name {
			return fmt.Errorf("已有同名的通道组「%s」", name)
		}
	}
	return nil
}

// GroupMembers returns the tunnels of a group ordered by listen port
func (cm *ConfigManager) GroupMembers(id string) []protocol.TunnelConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.groupMembersLocked(id)
}

func (cm *ConfigManager) groupMembersLocked(id string) []protocol.TunnelConfig {
	var out []protocol.TunnelConfig
	for _, t := range cm.cfg.Tunnels {
		if id != "" && t.GroupID == id {
			out = append(out, cloneTunnel(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ListenPort < out[j].ListenPort })
	return out
}

// GroupSpec describes a group's name and owner, and existing tunnels to move into it.
// 新通道统一在添加通道表单里创建（见 CreateTunnels）。
type GroupSpec struct {
	Name    string
	Owner   string
	MoveIDs []string
}

// SaveGroup creates a group from existing tunnels (id == "") or renames / adds members to one.
// Returns the group and the members whose config changed (moved in, or the group was renamed)
// — the caller pushes them to the relay server.
func (cm *ConfigManager) SaveGroup(id string, spec GroupSpec) (TunnelGroup, []protocol.TunnelConfig, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	spec.Owner = strings.TrimSpace(spec.Owner)

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if err := cm.checkGroupNameLocked(id, spec.Name); err != nil {
		return TunnelGroup{}, nil, err
	}
	gi := -1
	for i, g := range cm.cfg.Groups {
		if id != "" && g.ID == id {
			gi = i
		}
	}
	if id != "" && gi < 0 {
		return TunnelGroup{}, nil, fmt.Errorf("通道组不存在")
	}
	if id == "" && len(spec.MoveIDs) == 0 {
		return TunnelGroup{}, nil, fmt.Errorf("请选择要放进该组的通道")
	}

	group := TunnelGroup{ID: id, Name: spec.Name, Owner: spec.Owner, CreatedAt: time.Now()}
	if gi >= 0 {
		group.CreatedAt = cm.cfg.Groups[gi].CreatedAt
	} else {
		group.ID = uuid.New().String()
	}

	move := make(map[string]bool)
	for _, mid := range spec.MoveIDs {
		found := false
		for _, t := range cm.cfg.Tunnels {
			if t.ID != mid {
				continue
			}
			found = true
			if t.GroupID != "" && t.GroupID != group.ID {
				return TunnelGroup{}, nil, fmt.Errorf("「%s」已属于其它通道组，请先将其移出", t.Name)
			}
		}
		if !found {
			return TunnelGroup{}, nil, fmt.Errorf("通道 %s 未找到", mid)
		}
		move[mid] = true
	}

	renamed := gi >= 0 && cm.cfg.Groups[gi].Name != group.Name
	var changed []protocol.TunnelConfig
	for i, t := range cm.cfg.Tunnels {
		switch {
		case move[t.ID] && t.GroupID != group.ID:
			cm.cfg.Tunnels[i].GroupID = group.ID
			changed = append(changed, cloneTunnel(cm.cfg.Tunnels[i]))
		case renamed && t.GroupID == group.ID:
			changed = append(changed, cloneTunnel(t))
		}
	}
	if gi >= 0 {
		cm.cfg.Groups[gi] = group
	} else {
		cm.cfg.Groups = append(cm.cfg.Groups, group)
	}
	return group, changed, cm.save()
}

// RemoveGroup dissolves a group. With deleteMembers its tunnels are deleted too, otherwise they
// stay as standalone tunnels. Returns the affected members (as they are after the change).
func (cm *ConfigManager) RemoveGroup(id string, deleteMembers bool) ([]protocol.TunnelConfig, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	groups := make([]TunnelGroup, 0, len(cm.cfg.Groups))
	found := false
	for _, g := range cm.cfg.Groups {
		if g.ID == id {
			found = true
			continue
		}
		groups = append(groups, g)
	}
	if !found {
		return nil, fmt.Errorf("通道组不存在")
	}
	var affected []protocol.TunnelConfig
	kept := make([]protocol.TunnelConfig, 0, len(cm.cfg.Tunnels))
	for _, t := range cm.cfg.Tunnels {
		if t.GroupID != id {
			kept = append(kept, t)
			continue
		}
		t.GroupID = ""
		affected = append(affected, cloneTunnel(t))
		if !deleteMembers {
			kept = append(kept, t)
		}
	}
	cm.cfg.Groups = groups
	cm.cfg.Tunnels = kept
	return affected, cm.save()
}

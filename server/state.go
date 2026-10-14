package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DeepFal/PortRelay/core/protocol"
)

const (
	// 待审批申请的有效期；已处理的申请保留同样时长，供申请人页面查询结果
	requestTTL    = 30 * time.Minute
	inviteCodeLen = 10
	// leaseStartWindow 授权后需在多久内首次连接；授权时长从首次连接开始计算
	leaseStartWindow = 24 * time.Hour
)

// renewWindow 访问结束（授权到期或通道关闭）后多久内还能申请延长；之后清除记录，需要重新邀请
var renewWindow = 2 * time.Hour

var (
	errInviteNotFound = errors.New("邀请链接无效或已过期")
	errInviteUsedUp   = errors.New("该邀请链接的可用设备数已用完，请向管理员索取新的链接")
	errRequestGone    = errors.New("申请不存在或已过期")
	errAlreadyUsed    = errors.New("这台设备已经用过该邀请链接，请重新打开链接申请延长")
	errNothingToRenew = errors.New("访问已结束超过 2 小时，请联系管理员获取新的邀请链接")
	errRevoked        = errors.New("授权已被管理员撤销，如需继续使用请联系管理员")
	errNotStarted     = errors.New("授权尚未开始计时（首次连接后才开始），无需延长")
)

type leaseInfo struct {
	ExpiresAt time.Time `json:"expires_at"` // 计时开始前：首次连接截止时间；开始后：授权到期时间
	Name      string    `json:"name,omitempty"`
	Invite    string    `json:"invite,omitempty"`
	Duration  string    `json:"duration,omitempty"`
	// Deferred 为 true 表示尚未首次连接，计时未开始
	Deferred  bool       `json:"deferred,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// newLease creates a lease whose countdown starts at the device's first connection.
// Leases valid for the whole tunnel lifetime start immediately.
func newLease(name, invite, duration string, scopeExpires *time.Time, now time.Time) leaseInfo {
	l := leaseInfo{Name: name, Invite: invite, Duration: duration}
	if duration == protocol.LeaseDurationTunnel {
		l.StartedAt = &now
		l.ExpiresAt = leaseExpiry(duration, scopeExpires, now)
		return l
	}
	l.Deferred = true
	l.ExpiresAt = now.Add(leaseStartWindow)
	if scopeExpires != nil && scopeExpires.After(now) && scopeExpires.Before(l.ExpiresAt) {
		l.ExpiresAt = *scopeExpires
	}
	return l
}

// startLease starts the countdown of a deferred lease on the device's first connection.
// Returns the new expiry and true when the lease was started now.
func (s *stateStore) startLease(scope, ip string, scopeExpires *time.Time, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return time.Time{}, false
	}
	l, ok := st.Leases[ip]
	if !ok || !l.Deferred || !now.Before(l.ExpiresAt) {
		return time.Time{}, false
	}
	l.Deferred = false
	l.StartedAt = &now
	l.ExpiresAt = leaseExpiry(l.Duration, scopeExpires, now)
	st.Leases[ip] = l
	s.saveLocked()
	return l.ExpiresAt, true
}

func (s *stateStore) lease(scope, ip string) (leaseInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.data[scope]; st != nil {
		l, ok := st.Leases[ip]
		return l, ok && time.Now().Before(l.ExpiresAt)
	}
	return leaseInfo{}, false
}

// leaseRecord remembers a device whose access ended, so it can ask for an extension
type leaseRecord struct {
	EndedAt time.Time `json:"ended_at"`
	Name    string    `json:"name,omitempty"`
	Invite  string    `json:"invite,omitempty"`
	// Revoked 为 true 表示被管理员撤销，不能申请延长
	Revoked bool `json:"revoked,omitempty"`
}

func (r leaseRecord) renewable(now time.Time) bool {
	return !r.Revoked && now.Before(r.EndedAt.Add(renewWindow))
}

type tunnelState struct {
	Leases   map[string]leaseInfo               `json:"leases,omitempty"`   // ip -> lease
	Invites  map[string]*protocol.Invite        `json:"invites,omitempty"`  // code -> invite
	Requests map[string]*protocol.AccessRequest `json:"requests,omitempty"` // id -> request
	// Ended 访问已结束的设备（ip -> 记录），结束后 renewWindow 内可申请延长；通道关闭后仍保留
	Ended map[string]leaseRecord `json:"ended,omitempty"`
	// Name 通道或组最近的名称，通道关闭后用于申请延长页面
	Name string `json:"name,omitempty"`
}

// 授权状态的作用范围：单个通道（键为通道 ID，与旧数据兼容）或整个通道组（键为 "group:<组ID>"）
const groupScopePrefix = "group:"

func groupScope(groupID string) string { return groupScopePrefix + groupID }

// scopeOf returns the state key of a tunnel, or of its group when groupID is set
func scopeOf(tunnelID, groupID string) string {
	if groupID != "" {
		return groupScope(groupID)
	}
	return tunnelID
}

// splitScope returns the tunnel or group ID of a scope and whether it is a group scope
func splitScope(scope string) (id string, isGroup bool) {
	if strings.HasPrefix(scope, groupScopePrefix) {
		return strings.TrimPrefix(scope, groupScopePrefix), true
	}
	return scope, false
}

// stateStore keeps per-scope access state (leases, invites, approval requests).
// It lives in memory and is optionally persisted to a file so it survives restarts.
type stateStore struct {
	path string
	mu   sync.Mutex
	data map[string]*tunnelState // scope（通道 ID 或 group:<组ID>）-> state
}

func newStateStore() *stateStore {
	return &stateStore{data: make(map[string]*tunnelState)}
}

// EnableLeasePersistence loads access state from path and keeps it updated
func (m *Manager) EnableLeasePersistence(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	data := make(map[string]*tunnelState)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &data); err != nil || !looksLikeState(data) {
			// 兼容旧格式：通道 ID -> ip -> 到期时间
			var legacy map[string]map[string]time.Time
			if json.Unmarshal(raw, &legacy) == nil {
				data = make(map[string]*tunnelState)
				for id, ips := range legacy {
					st := &tunnelState{Leases: make(map[string]leaseInfo)}
					for ip, exp := range ips {
						st.Leases[ip] = leaseInfo{ExpiresAt: exp}
					}
					data[id] = st
				}
			} else {
				log.Printf("[WebAuth] ignoring unreadable state file %s: %v", path, err)
				data = make(map[string]*tunnelState)
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}

	s := m.state
	s.mu.Lock()
	s.path = path
	s.data = data
	s.pruneLocked()
	n := 0
	for _, st := range s.data {
		n += len(st.Leases)
	}
	s.mu.Unlock()
	if n > 0 {
		log.Printf("[WebAuth] Loaded %d unexpired temporary lease(s) from %s", n, path)
	}
	return nil
}

// looksLikeState distinguishes the new format from the legacy one (both are JSON objects of objects)
func looksLikeState(d map[string]*tunnelState) bool {
	for _, st := range d {
		if st == nil || (st.Leases == nil && st.Invites == nil && st.Requests == nil && st.Ended == nil) {
			return false
		}
	}
	return true
}

func (s *stateStore) scopeLocked(id string) *tunnelState {
	st := s.data[id]
	if st == nil {
		st = &tunnelState{}
		s.data[id] = st
	}
	if st.Leases == nil {
		st.Leases = make(map[string]leaseInfo)
	}
	if st.Invites == nil {
		st.Invites = make(map[string]*protocol.Invite)
	}
	if st.Requests == nil {
		st.Requests = make(map[string]*protocol.AccessRequest)
	}
	if st.Ended == nil {
		st.Ended = make(map[string]leaseRecord)
	}
	return st
}

// endLeaseLocked turns a lease into an ended record (renewable unless revoked)
func endLeaseLocked(st *tunnelState, ip string, l leaseInfo, at time.Time, revoked bool) {
	if st.Ended == nil {
		st.Ended = make(map[string]leaseRecord)
	}
	st.Ended[ip] = leaseRecord{EndedAt: at, Name: l.Name, Invite: l.Invite, Revoked: revoked}
	delete(st.Leases, ip)
}

func randomCode(n int) string {
	const chars = "23456789abcdefghjkmnpqrstuvwxyz"
	b := make([]byte, n)
	for i := range b {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			panic(err)
		}
		b[i] = chars[v.Int64()]
	}
	return string(b)
}

// ---- leases ----

// removeLease removes one lease, or all leases of the scope when ip is empty; returns how many were removed
func (s *stateStore) removeLease(scope, ip string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return 0
	}
	// 被撤销的授权不能再申请延长
	now := time.Now()
	n := 0
	for lip, l := range st.Leases {
		if ip == "" || lip == ip {
			endLeaseLocked(st, lip, l, now, true)
			n++
		}
	}
	for lip, r := range st.Ended {
		if ip == "" || lip == ip {
			r.Revoked = true
			st.Ended[lip] = r
		}
	}
	for id, r := range st.Requests {
		if r.Renewal && r.Status == "pending" && (ip == "" || r.IP == ip) {
			delete(st.Requests, id)
		}
	}
	s.saveLocked()
	return n
}

func (s *stateStore) leaseExpiries(scope string) map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := make(map[string]time.Time)
	if st := s.data[scope]; st != nil {
		for ip, l := range st.Leases {
			if now.Before(l.ExpiresAt) {
				out[ip] = l.ExpiresAt
			}
		}
	}
	return out
}

func (s *stateStore) leaseList(scope string) []protocol.DynamicLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []protocol.DynamicLease
	if st := s.data[scope]; st != nil {
		for ip, l := range st.Leases {
			if now.Before(l.ExpiresAt) {
				out = append(out, protocol.DynamicLease{
					IP: ip, ExpiresAt: l.ExpiresAt, Name: l.Name, Invite: l.Invite,
					Duration: l.Duration, AwaitingConnect: l.Deferred, StartedAt: l.StartedAt,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out
}

// closeScope runs when a tunnel, or the last open tunnel of a group, is closed: invites and
// first-time requests become invalid, running leases end — but ended devices are remembered for
// renewWindow so they can still ask for an extension.
func (s *stateStore) closeScope(scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return
	}
	now := time.Now()
	for ip, l := range st.Leases {
		end := now
		if l.ExpiresAt.Before(now) {
			end = l.ExpiresAt
		}
		endLeaseLocked(st, ip, l, end, false)
	}
	st.Invites = nil
	for id, r := range st.Requests {
		if !r.Renewal && r.Status == "pending" {
			delete(st.Requests, id)
		}
	}
	s.saveLocked()
}

// scopeDisplayName is the last known name of a scope (for pages shown after it closed)
func (s *stateStore) scopeDisplayName(scope string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.data[scope]; st != nil {
		return st.Name
	}
	return ""
}

// setName records the display name of a scope that has access state
func (s *stateStore) setName(scope, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.data[scope]; st != nil && name != "" && st.Name != name {
		st.Name = name
	}
}

// dropScope drops all access state of a deleted tunnel or a dissolved group, renewal records included
func (s *stateStore) dropScope(scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[scope]; ok {
		delete(s.data, scope)
		s.saveLocked()
	}
}

// ---- invites ----

func (s *stateStore) createInvite(scope, note string, valid time.Duration, maxUses int, leaseDuration string, requireApproval bool) protocol.Invite {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if leaseDuration == "" {
		leaseDuration = protocol.LeaseDuration2h
	}
	inv := &protocol.Invite{
		Code:            randomCode(inviteCodeLen),
		Note:            note,
		CreatedAt:       now,
		ExpiresAt:       now.Add(valid),
		MaxUses:         maxUses,
		LeaseDuration:   leaseDuration,
		RequireApproval: requireApproval,
	}
	s.scopeLocked(scope).Invites[inv.Code] = inv
	s.saveLocked()
	return *inv
}

// findInvite returns the scope (tunnel or group) owning an unexpired invite
func (s *stateStore) findInvite(code string) (string, protocol.Invite, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, st := range s.data {
		if inv, ok := st.Invites[code]; ok && now.Before(inv.ExpiresAt) {
			return id, *inv, true
		}
	}
	return "", protocol.Invite{}, false
}

// capacityOKLocked reports whether ip may use the invite: devices that already used it
// (or have a pending request through it) may come back; new devices need a free slot.
func capacityOKLocked(st *tunnelState, inv *protocol.Invite, ip string) bool {
	if inv.MaxUses <= 0 {
		return true
	}
	devices := make(map[string]bool)
	for _, u := range inv.Uses {
		devices[u.IP] = true
	}
	for _, r := range st.Requests {
		if r.Invite == inv.Code && r.Status == "pending" {
			devices[r.IP] = true
		}
	}
	return devices[ip] || len(devices) < inv.MaxUses
}

// checkInvite validates that ip may use the invite right now
func (s *stateStore) checkInvite(scope, code, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return errInviteNotFound
	}
	inv, ok := st.Invites[code]
	if !ok || !time.Now().Before(inv.ExpiresAt) {
		return errInviteNotFound
	}
	if !capacityOKLocked(st, inv, ip) {
		return errInviteUsedUp
	}
	return nil
}

func (s *stateStore) recordUseLocked(st *tunnelState, code, name, ip string) {
	inv, ok := st.Invites[code]
	if !ok {
		return
	}
	for i, u := range inv.Uses {
		if u.IP == ip {
			inv.Uses[i] = protocol.InviteUse{Name: name, IP: ip, At: time.Now()}
			return
		}
	}
	inv.Uses = append(inv.Uses, protocol.InviteUse{Name: name, IP: ip, At: time.Now()})
}

// useInviteAndGrant records the use and stores the lease atomically; returns the lease
func (s *stateStore) useInviteAndGrant(scope, code, name, ip string, scopeExpires *time.Time) (leaseInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return leaseInfo{}, errInviteNotFound
	}
	inv, ok := st.Invites[code]
	if !ok || !time.Now().Before(inv.ExpiresAt) {
		return leaseInfo{}, errInviteNotFound
	}
	// 已授权的设备再次提交不重新计时；用过链接、授权已结束的设备要申请延长
	if l, ok := st.Leases[ip]; ok && time.Now().Before(l.ExpiresAt) {
		return l, nil
	}
	if usedByLocked(inv, ip) {
		return leaseInfo{}, errAlreadyUsed
	}
	if !capacityOKLocked(st, inv, ip) {
		return leaseInfo{}, errInviteUsedUp
	}
	st = s.scopeLocked(scope)
	s.recordUseLocked(st, code, name, ip)
	l := newLease(name, code, inv.LeaseDuration, scopeExpires, time.Now())
	st.Leases[ip] = l
	delete(st.Ended, ip)
	s.saveLocked()
	return l, nil
}

func usedByLocked(inv *protocol.Invite, ip string) bool {
	for _, u := range inv.Uses {
		if u.IP == ip {
			return true
		}
	}
	return false
}

// revokeInvite removes the invite and the leases it granted; returns the affected IPs
func (s *stateStore) revokeInvite(scope, code string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return nil
	}
	delete(st.Invites, code)
	var ips []string
	now := time.Now()
	for ip, l := range st.Leases {
		if l.Invite == code {
			ips = append(ips, ip)
			endLeaseLocked(st, ip, l, now, true)
		}
	}
	for ip, r := range st.Ended {
		if r.Invite == code {
			r.Revoked = true
			st.Ended[ip] = r
		}
	}
	for id, r := range st.Requests {
		if r.Invite == code && r.Status == "pending" {
			delete(st.Requests, id)
		}
	}
	s.saveLocked()
	return ips
}

func (s *stateStore) inviteList(scope string) []protocol.Invite {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []protocol.Invite
	if st := s.data[scope]; st != nil {
		for _, inv := range st.Invites {
			if now.Before(inv.ExpiresAt) {
				c := *inv
				c.Uses = append([]protocol.InviteUse(nil), inv.Uses...)
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ---- approval requests ----

// addRequest files a pending request (re-using an existing pending one from the same IP)
func (s *stateStore) addRequest(scope, code, name, ip string) (protocol.AccessRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[scope]
	if st == nil {
		return protocol.AccessRequest{}, errInviteNotFound
	}
	inv, ok := st.Invites[code]
	if !ok || !time.Now().Before(inv.ExpiresAt) {
		return protocol.AccessRequest{}, errInviteNotFound
	}
	st = s.scopeLocked(scope)
	for _, r := range st.Requests {
		if r.IP == ip && r.Status == "pending" && !r.Renewal {
			r.Name = name
			s.saveLocked()
			return *r, nil
		}
	}
	if usedByLocked(inv, ip) {
		return protocol.AccessRequest{}, errAlreadyUsed
	}
	if !capacityOKLocked(st, inv, ip) {
		return protocol.AccessRequest{}, errInviteUsedUp
	}
	r := &protocol.AccessRequest{
		ID:     randomCode(16),
		Name:   name,
		IP:     ip,
		Invite: code,
		At:     time.Now(),
		Status: "pending",
	}
	if id, isGroup := splitScope(scope); isGroup {
		r.GroupID = id
	} else {
		r.TunnelID = id
	}
	st.Requests[r.ID] = r
	s.saveLocked()
	return *r, nil
}

func (s *stateStore) request(id string) (protocol.AccessRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRequestsLocked()
	for _, st := range s.data {
		if r, ok := st.Requests[id]; ok {
			return *r, true
		}
	}
	return protocol.AccessRequest{}, false
}

// decide marks a pending request; on approval it records the invite use and stores the lease,
// lasting as long as the originating invite specifies (capped by the tunnel's own TTL).
func (s *stateStore) decide(scope, id string, approve bool, scopeExpires *time.Time) (protocol.AccessRequest, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRequestsLocked()
	st := s.data[scope]
	if st == nil {
		return protocol.AccessRequest{}, time.Time{}, errRequestGone
	}
	r, ok := st.Requests[id]
	if !ok || r.Status != "pending" {
		return protocol.AccessRequest{}, time.Time{}, errRequestGone
	}
	st = s.scopeLocked(scope)
	duration := ""
	if inv, ok := st.Invites[r.Invite]; ok {
		duration = inv.LeaseDuration
	}
	if r.Renewal {
		// 延长申请通常由 extendLease 按管理员选择的时长批准；这里按邀请的时长（默认 2 小时）处理
		if !approve {
			r.Status, r.At = "rejected", time.Now()
			s.saveLocked()
			return *r, time.Time{}, nil
		}
		minutes, ok := protocol.LeaseMinutes(duration)
		if !ok {
			minutes = 120
		}
		exp, err := s.extendLocked(st, r.IP, minutes, time.Now())
		if err != nil {
			return protocol.AccessRequest{}, time.Time{}, err
		}
		s.saveLocked()
		return *r, exp, nil
	}
	l := newLease(r.Name, r.Invite, duration, scopeExpires, time.Now())
	exp := l.ExpiresAt
	r.At = time.Now()
	if approve {
		r.Status = "approved"
		s.recordUseLocked(st, r.Invite, r.Name, r.IP)
		st.Leases[r.IP] = l
		delete(st.Ended, r.IP)
	} else {
		r.Status = "rejected"
	}
	s.saveLocked()
	return *r, exp, nil
}

func (s *stateStore) pendingList(scope string) []protocol.AccessRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRequestsLocked()
	var out []protocol.AccessRequest
	if st := s.data[scope]; st != nil {
		for _, r := range st.Requests {
			if r.Status == "pending" && !r.Renewal {
				out = append(out, *r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// ---- renewals ----

// deviceAccess is what a device that opens an invite link already has in the invite's scope
type deviceAccess struct {
	scope   string
	name    string // 通道或组的名称（通道关闭后为最近一次的名称）
	lease   *leaseInfo
	ended   *leaseRecord
	pending *protocol.AccessRequest // 待处理的延长申请
	// usedBefore 用过该链接，但访问已结束超过 renewWindow
	usedBefore bool
}

// deviceAccess looks up ip's access in the scope of invite code, also after the invite or the
// tunnel is gone; false when the device never used this invite (a new visitor)
func (s *stateStore) deviceAccess(code, ip string) (deviceAccess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	now := time.Now()
	for scope, st := range s.data {
		inv := st.Invites[code]
		l, hasLease := st.Leases[ip]
		rec, hasRec := st.Ended[ip]
		mine := (inv != nil && usedByLocked(inv, ip)) || (hasLease && l.Invite == code) || (hasRec && rec.Invite == code)
		if !mine {
			continue
		}
		da := deviceAccess{scope: scope, name: st.Name}
		switch {
		case hasLease && now.Before(l.ExpiresAt):
			da.lease = &l
		case hasRec && (rec.Revoked || rec.renewable(now)):
			da.ended = &rec
		default:
			da.usedBefore = true
		}
		for _, r := range st.Requests {
			if r.IP == ip && r.Renewal && r.Status == "pending" {
				c := *r
				da.pending = &c
			}
		}
		return da, true
	}
	return deviceAccess{}, false
}

// addRenewal files (or re-uses) a renewal request for ip: its lease must be running, or have
// ended less than renewWindow ago without being revoked
func (s *stateStore) addRenewal(scope, ip string) (protocol.AccessRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	st := s.data[scope]
	if st == nil {
		return protocol.AccessRequest{}, errNothingToRenew
	}
	now := time.Now()
	var name, invite string
	if l, ok := st.Leases[ip]; ok && now.Before(l.ExpiresAt) {
		if l.Deferred {
			return protocol.AccessRequest{}, errNotStarted
		}
		name, invite = l.Name, l.Invite
	} else if rec, ok := st.Ended[ip]; ok && rec.Revoked {
		return protocol.AccessRequest{}, errRevoked
	} else if ok && rec.renewable(now) {
		name, invite = rec.Name, rec.Invite
	} else {
		return protocol.AccessRequest{}, errNothingToRenew
	}
	st = s.scopeLocked(scope)
	for _, r := range st.Requests {
		if r.IP == ip && r.Renewal && r.Status == "pending" {
			return *r, nil
		}
	}
	r := &protocol.AccessRequest{ID: randomCode(16), Name: name, IP: ip, Invite: invite, At: now, Status: "pending", Renewal: true}
	if id, isGroup := splitScope(scope); isGroup {
		r.GroupID = id
	} else {
		r.TunnelID = id
	}
	st.Requests[r.ID] = r
	s.saveLocked()
	return *r, nil
}

// extendLease extends ip's access in scope by minutes and approves its pending renewal requests.
// 不受通道最长开启时长限制：管理员批准的时长优先。
func (s *stateStore) extendLease(scope, ip string, minutes int) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	st := s.data[scope]
	if st == nil {
		return time.Time{}, errNothingToRenew
	}
	exp, err := s.extendLocked(s.scopeLocked(scope), ip, minutes, time.Now())
	if err != nil {
		return time.Time{}, err
	}
	s.saveLocked()
	return exp, nil
}

// extendLocked: a running lease is extended from its expiry (one not yet started starts now),
// an ended one is restored from now. Caller must hold s.mu and save.
func (s *stateStore) extendLocked(st *tunnelState, ip string, minutes int, now time.Time) (time.Time, error) {
	d := time.Duration(minutes) * time.Minute
	if l, ok := st.Leases[ip]; ok && now.Before(l.ExpiresAt) {
		if l.Deferred {
			l.Deferred, l.StartedAt, l.ExpiresAt = false, &now, now.Add(d)
		} else {
			l.ExpiresAt = l.ExpiresAt.Add(d)
		}
		st.Leases[ip] = l
	} else if rec, ok := st.Ended[ip]; ok && rec.Revoked {
		return time.Time{}, errRevoked
	} else if ok && rec.renewable(now) {
		st.Leases[ip] = leaseInfo{Name: rec.Name, Invite: rec.Invite, Duration: protocol.LeaseDurationOf(minutes), StartedAt: &now, ExpiresAt: now.Add(d)}
		delete(st.Ended, ip)
	} else {
		return time.Time{}, errNothingToRenew
	}
	for _, r := range st.Requests {
		if r.IP == ip && r.Renewal && r.Status == "pending" {
			r.Status, r.At = "approved", now
		}
	}
	return st.Leases[ip].ExpiresAt, nil
}

// renewalList reports, across all scopes (closed ones included), the devices that can still be
// extended and the pending renewal requests
func (s *stateStore) renewalList() []protocol.RenewalInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	now := time.Now()
	var out []protocol.RenewalInfo
	for scope, st := range s.data {
		id, isGroup := splitScope(scope)
		base := protocol.RenewalInfo{TunnelID: id}
		if isGroup {
			base = protocol.RenewalInfo{GroupID: id}
		}
		byIP := make(map[string]int)
		for ip, rec := range st.Ended {
			if !rec.renewable(now) {
				continue
			}
			info := base
			end, before := rec.EndedAt, rec.EndedAt.Add(renewWindow)
			info.IP, info.Name, info.Invite, info.EndedAt, info.RenewBefore = ip, rec.Name, rec.Invite, &end, &before
			byIP[ip] = len(out)
			out = append(out, info)
		}
		for _, r := range st.Requests {
			if !r.Renewal || r.Status != "pending" {
				continue
			}
			c := *r
			if i, ok := byIP[r.IP]; ok {
				out[i].Request = &c
				continue
			}
			info := base
			info.IP, info.Name, info.Invite, info.Request = r.IP, r.Name, r.Invite, &c
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Request != nil) != (out[j].Request != nil) {
			return out[i].Request != nil
		}
		ti, tj := now, now
		if out[i].EndedAt != nil {
			ti = *out[i].EndedAt
		}
		if out[j].EndedAt != nil {
			tj = *out[j].EndedAt
		}
		return ti.After(tj)
	})
	return out
}

// ---- housekeeping ----

func (s *stateStore) pruneRequestsLocked() {
	cutoff := time.Now().Add(-requestTTL)
	for _, st := range s.data {
		for id, r := range st.Requests {
			if r.At.Before(cutoff) {
				delete(st.Requests, id)
			}
		}
	}
}

func (s *stateStore) pruneLocked() {
	now := time.Now()
	s.pruneRequestsLocked()
	for id, st := range s.data {
		if st == nil {
			delete(s.data, id)
			continue
		}
		for ip, l := range st.Leases {
			if !now.Before(l.ExpiresAt) {
				endLeaseLocked(st, ip, l, l.ExpiresAt, false)
			}
		}
		for ip, r := range st.Ended {
			if !now.Before(r.EndedAt.Add(renewWindow)) {
				delete(st.Ended, ip)
			}
		}
		for code, inv := range st.Invites {
			if !now.Before(inv.ExpiresAt) {
				delete(st.Invites, code)
			}
		}
		if len(st.Leases) == 0 && len(st.Invites) == 0 && len(st.Requests) == 0 && len(st.Ended) == 0 {
			delete(s.data, id)
		}
	}
}

func (s *stateStore) saveLocked() {
	s.pruneLocked()
	if s.path == "" {
		return
	}
	data, err := json.Marshal(s.data)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.json")
	if err != nil {
		log.Printf("[WebAuth] save state: %v", err)
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		log.Printf("[WebAuth] save state: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		log.Printf("[WebAuth] save state: %v", err)
	}
}

// leaseExpiry computes when a lease granted now ends, per the invite's lease duration,
// never outliving the tunnel's own TTL.
func leaseExpiry(duration string, scopeExpires *time.Time, now time.Time) time.Time {
	var exp time.Time
	switch duration {
	case protocol.LeaseDurationToday:
		y, mo, d := now.Date()
		exp = time.Date(y, mo, d, 23, 59, 59, 0, now.Location())
	case protocol.LeaseDurationTunnel:
		// 通道开启期间有效：通道被主动关闭时放通会被清除；设一个上限避免永久放通
		exp = now.Add(30 * 24 * time.Hour)
	default:
		minutes, ok := protocol.LeaseMinutes(duration)
		if !ok {
			minutes = 120
		}
		exp = now.Add(time.Duration(minutes) * time.Minute)
	}
	if scopeExpires != nil && scopeExpires.After(now) && scopeExpires.Before(exp) {
		exp = *scopeExpires
	}
	return exp
}

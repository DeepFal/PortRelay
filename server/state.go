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

var (
	errInviteNotFound = errors.New("邀请链接无效或已过期")
	errInviteUsedUp   = errors.New("该邀请链接的可用设备数已用完，请向管理员索取新的链接")
	errRequestGone    = errors.New("申请不存在或已过期")
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
func newLease(name, invite, duration string, tunnelExpires *time.Time, now time.Time) leaseInfo {
	l := leaseInfo{Name: name, Invite: invite, Duration: duration}
	if duration == protocol.LeaseDurationTunnel {
		l.StartedAt = &now
		l.ExpiresAt = leaseExpiry(duration, tunnelExpires, now)
		return l
	}
	l.Deferred = true
	l.ExpiresAt = now.Add(leaseStartWindow)
	if tunnelExpires != nil && tunnelExpires.After(now) && tunnelExpires.Before(l.ExpiresAt) {
		l.ExpiresAt = *tunnelExpires
	}
	return l
}

// startLease starts the countdown of a deferred lease on the device's first connection.
// Returns the new expiry and true when the lease was started now.
func (s *stateStore) startLease(tunnelID, ip string, tunnelExpires *time.Time, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[tunnelID]
	if st == nil {
		return time.Time{}, false
	}
	l, ok := st.Leases[ip]
	if !ok || !l.Deferred || !now.Before(l.ExpiresAt) {
		return time.Time{}, false
	}
	l.Deferred = false
	l.StartedAt = &now
	l.ExpiresAt = leaseExpiry(l.Duration, tunnelExpires, now)
	st.Leases[ip] = l
	s.saveLocked()
	return l.ExpiresAt, true
}

func (s *stateStore) lease(tunnelID, ip string) (leaseInfo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.data[tunnelID]; st != nil {
		l, ok := st.Leases[ip]
		return l, ok && time.Now().Before(l.ExpiresAt)
	}
	return leaseInfo{}, false
}

type tunnelState struct {
	Leases   map[string]leaseInfo               `json:"leases,omitempty"`   // ip -> lease
	Invites  map[string]*protocol.Invite        `json:"invites,omitempty"`  // code -> invite
	Requests map[string]*protocol.AccessRequest `json:"requests,omitempty"` // id -> request
}

// stateStore keeps per-tunnel access state (leases, invites, approval requests).
// It lives in memory and is optionally persisted to a file so it survives restarts.
type stateStore struct {
	path string
	mu   sync.Mutex
	data map[string]*tunnelState // tunnelID -> state
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
			// 兼容旧格式：tunnelID -> ip -> 到期时间
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
		if st == nil || (st.Leases == nil && st.Invites == nil && st.Requests == nil) {
			return false
		}
	}
	return true
}

func (s *stateStore) tunnelLocked(id string) *tunnelState {
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
	return st
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

func (s *stateStore) grantLease(tunnelID, ip string, exp time.Time, name, invite string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tunnelLocked(tunnelID).Leases[ip] = leaseInfo{ExpiresAt: exp, Name: name, Invite: invite}
	s.saveLocked()
}

// removeLease removes one lease, or all leases of the tunnel when ip is empty
func (s *stateStore) removeLease(tunnelID, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[tunnelID]
	if st == nil {
		return
	}
	if ip == "" {
		st.Leases = make(map[string]leaseInfo)
	} else {
		delete(st.Leases, ip)
	}
	s.saveLocked()
}

func (s *stateStore) leaseExpiries(tunnelID string) map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := make(map[string]time.Time)
	if st := s.data[tunnelID]; st != nil {
		for ip, l := range st.Leases {
			if now.Before(l.ExpiresAt) {
				out[ip] = l.ExpiresAt
			}
		}
	}
	return out
}

func (s *stateStore) leaseList(tunnelID string) []protocol.DynamicLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []protocol.DynamicLease
	if st := s.data[tunnelID]; st != nil {
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

// forgetTunnel drops all access state of a tunnel (explicitly closed or deleted)
func (s *stateStore) forgetTunnel(tunnelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[tunnelID]; ok {
		delete(s.data, tunnelID)
		s.saveLocked()
	}
}

// ---- invites ----

func (s *stateStore) createInvite(tunnelID, note string, valid time.Duration, maxUses int, leaseDuration string, requireApproval bool) protocol.Invite {
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
	s.tunnelLocked(tunnelID).Invites[inv.Code] = inv
	s.saveLocked()
	return *inv
}

// findInvite returns the tunnel owning an unexpired invite
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
func (s *stateStore) checkInvite(tunnelID, code, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[tunnelID]
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
func (s *stateStore) useInviteAndGrant(tunnelID, code, name, ip string, tunnelExpires *time.Time) (leaseInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[tunnelID]
	if st == nil {
		return leaseInfo{}, errInviteNotFound
	}
	inv, ok := st.Invites[code]
	if !ok || !time.Now().Before(inv.ExpiresAt) {
		return leaseInfo{}, errInviteNotFound
	}
	if !capacityOKLocked(st, inv, ip) {
		return leaseInfo{}, errInviteUsedUp
	}
	st = s.tunnelLocked(tunnelID)
	s.recordUseLocked(st, code, name, ip)
	l := newLease(name, code, inv.LeaseDuration, tunnelExpires, time.Now())
	st.Leases[ip] = l
	s.saveLocked()
	return l, nil
}

// revokeInvite removes the invite and the leases it granted; returns the affected IPs
func (s *stateStore) revokeInvite(tunnelID, code string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[tunnelID]
	if st == nil {
		return nil
	}
	delete(st.Invites, code)
	var ips []string
	for ip, l := range st.Leases {
		if l.Invite == code {
			ips = append(ips, ip)
			delete(st.Leases, ip)
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

func (s *stateStore) inviteList(tunnelID string) []protocol.Invite {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []protocol.Invite
	if st := s.data[tunnelID]; st != nil {
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
func (s *stateStore) addRequest(tunnelID, code, name, ip string) (protocol.AccessRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.data[tunnelID]
	if st == nil {
		return protocol.AccessRequest{}, errInviteNotFound
	}
	inv, ok := st.Invites[code]
	if !ok || !time.Now().Before(inv.ExpiresAt) {
		return protocol.AccessRequest{}, errInviteNotFound
	}
	st = s.tunnelLocked(tunnelID)
	for _, r := range st.Requests {
		if r.IP == ip && r.Status == "pending" {
			r.Name = name
			s.saveLocked()
			return *r, nil
		}
	}
	if !capacityOKLocked(st, inv, ip) {
		return protocol.AccessRequest{}, errInviteUsedUp
	}
	r := &protocol.AccessRequest{
		ID:       randomCode(16),
		TunnelID: tunnelID,
		Name:     name,
		IP:       ip,
		Invite:   code,
		At:       time.Now(),
		Status:   "pending",
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
func (s *stateStore) decide(tunnelID, id string, approve bool, tunnelExpires *time.Time) (protocol.AccessRequest, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRequestsLocked()
	st := s.data[tunnelID]
	if st == nil {
		return protocol.AccessRequest{}, time.Time{}, errRequestGone
	}
	r, ok := st.Requests[id]
	if !ok || r.Status != "pending" {
		return protocol.AccessRequest{}, time.Time{}, errRequestGone
	}
	st = s.tunnelLocked(tunnelID)
	duration := ""
	if inv, ok := st.Invites[r.Invite]; ok {
		duration = inv.LeaseDuration
	}
	l := newLease(r.Name, r.Invite, duration, tunnelExpires, time.Now())
	exp := l.ExpiresAt
	r.At = time.Now()
	if approve {
		r.Status = "approved"
		s.recordUseLocked(st, r.Invite, r.Name, r.IP)
		st.Leases[r.IP] = l
	} else {
		r.Status = "rejected"
	}
	s.saveLocked()
	return *r, exp, nil
}

func (s *stateStore) pendingList(tunnelID string) []protocol.AccessRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRequestsLocked()
	var out []protocol.AccessRequest
	if st := s.data[tunnelID]; st != nil {
		for _, r := range st.Requests {
			if r.Status == "pending" {
				out = append(out, *r)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
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
				delete(st.Leases, ip)
			}
		}
		for code, inv := range st.Invites {
			if !now.Before(inv.ExpiresAt) {
				delete(st.Invites, code)
			}
		}
		if len(st.Leases) == 0 && len(st.Invites) == 0 && len(st.Requests) == 0 {
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
func leaseExpiry(duration string, tunnelExpires *time.Time, now time.Time) time.Time {
	var exp time.Time
	switch duration {
	case protocol.LeaseDuration1h:
		exp = now.Add(time.Hour)
	case protocol.LeaseDuration8h:
		exp = now.Add(8 * time.Hour)
	case protocol.LeaseDurationToday:
		y, mo, d := now.Date()
		exp = time.Date(y, mo, d, 23, 59, 59, 0, now.Location())
	case protocol.LeaseDurationTunnel:
		// 通道开启期间有效：通道被主动关闭时放通会被清除；设一个上限避免永久放通
		exp = now.Add(30 * 24 * time.Hour)
	default:
		exp = now.Add(2 * time.Hour)
	}
	if tunnelExpires != nil && tunnelExpires.After(now) && tunnelExpires.Before(exp) {
		exp = *tunnelExpires
	}
	return exp
}

package acl

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Whitelist checks if an IP is allowed based on rules and dynamic leases
type Whitelist struct {
	rules    []string
	allowAll bool
	ips      map[string]bool
	subnets  []*net.IPNet
	dynamic  map[string]time.Time // dynamic IP -> expiration
	mu       sync.RWMutex
}

// NewWhitelist parses a list of rules
func NewWhitelist(rules []string) *Whitelist {
	w := &Whitelist{
		rules:   rules,
		ips:     make(map[string]bool),
		subnets: make([]*net.IPNet, 0),
		dynamic: make(map[string]time.Time),
	}

	for _, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if r == "*" || r == "0.0.0.0/0" || r == "::/0" {
			w.allowAll = true
			continue
		}
		if strings.Contains(r, "/") {
			_, ipNet, err := net.ParseCIDR(r)
			if err == nil {
				w.subnets = append(w.subnets, ipNet)
			}
		} else {
			ip := net.ParseIP(r)
			if ip != nil {
				w.ips[ip.String()] = true
			}
		}
	}
	return w
}

// ValidateRule reports whether a whitelist rule is "*", a valid IP or a valid CIDR
func ValidateRule(r string) error {
	r = strings.TrimSpace(r)
	switch {
	case r == "":
		return fmt.Errorf("白名单规则不能为空")
	case r == "*":
		return nil
	case strings.Contains(r, "/"):
		if _, _, err := net.ParseCIDR(r); err != nil {
			return fmt.Errorf("无效的网段 %q", r)
		}
	default:
		if net.ParseIP(r) == nil {
			return fmt.Errorf("无效的 IP 地址 %q", r)
		}
	}
	return nil
}

// DynamicLeases returns a copy of unexpired dynamic leases
func (w *Whitelist) DynamicLeases() map[string]time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	now := time.Now()
	out := make(map[string]time.Time, len(w.dynamic))
	for ip, exp := range w.dynamic {
		if now.Before(exp) {
			out[ip] = exp
		}
	}
	return out
}

// RestoreDynamic merges previously granted leases (e.g. after a config update)
func (w *Whitelist) RestoreDynamic(leases map[string]time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ip, exp := range leases {
		if cur, ok := w.dynamic[ip]; !ok || exp.After(cur) {
			w.dynamic[ip] = exp
		}
	}
}

// ReplaceDynamic sets the dynamic leases to exactly the given set (the access state store is the source of truth)
func (w *Whitelist) ReplaceDynamic(leases map[string]time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dynamic = make(map[string]time.Time, len(leases))
	for ip, exp := range leases {
		if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed != nil {
			w.dynamic[parsed.String()] = exp
		}
	}
}

// RemoveDynamic revokes the lease for ip (all leases when ip is empty) and returns how many were removed
func (w *Whitelist) RemoveDynamic(ip string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ip == "" {
		n := len(w.dynamic)
		w.dynamic = make(map[string]time.Time)
		return n
	}
	if parsed := net.ParseIP(strings.TrimSpace(ip)); parsed != nil {
		if _, ok := w.dynamic[parsed.String()]; ok {
			delete(w.dynamic, parsed.String())
			return 1
		}
	}
	return 0
}

// AddDynamic adds an IP with an expiration timestamp
func (w *Whitelist) AddDynamic(ipStr string, expireAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	host := ipStr
	if h, _, err := net.SplitHostPort(ipStr); err == nil {
		host = h
	}
	parsed := net.ParseIP(host)
	if parsed != nil {
		w.dynamic[parsed.String()] = expireAt
	}
}

// GetDynamic returns active unexpired dynamic IPs
func (w *Whitelist) GetDynamic() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	var list []string
	for ip, exp := range w.dynamic {
		if now.Before(exp) {
			list = append(list, ip)
		} else {
			delete(w.dynamic, ip)
		}
	}
	return list
}

// IsAllowed checks if remoteAddr (either "IP" or "IP:Port") is permitted
func (w *Whitelist) IsAllowed(remoteAddr string) (bool, string) {
	if w.allowAll {
		return true, ""
	}

	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}

	parsedIP := net.ParseIP(host)
	if parsedIP == nil {
		return false, "invalid client IP"
	}

	ipStr := parsedIP.String()

	// Canonical IP string check
	if w.ips[ipStr] {
		return true, ""
	}

	// Dynamic leases check
	w.mu.RLock()
	if exp, ok := w.dynamic[ipStr]; ok {
		if time.Now().Before(exp) {
			w.mu.RUnlock()
			return true, ""
		}
	}
	w.mu.RUnlock()

	// CIDR subnets check
	for _, subnet := range w.subnets {
		if subnet.Contains(parsedIP) {
			return true, ""
		}
	}

	return false, "IP not in whitelist"
}

// Rules returns the raw rules
func (w *Whitelist) Rules() []string {
	return w.rules
}

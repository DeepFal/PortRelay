package acl

import (
	"testing"
	"time"
)

func TestWhitelist(t *testing.T) {
	tests := []struct {
		name     string
		rules    []string
		ip       string
		expected bool
	}{
		{
			name:     "allow all wildcard",
			rules:    []string{"*"},
			ip:       "192.168.1.50:12345",
			expected: true,
		},
		{
			name:     "single ip match",
			rules:    []string{"192.168.1.50"},
			ip:       "192.168.1.50:12345",
			expected: true,
		},
		{
			name:     "single ip mismatch",
			rules:    []string{"192.168.1.50"},
			ip:       "192.168.1.51:12345",
			expected: false,
		},
		{
			name:     "cidr match",
			rules:    []string{"192.168.1.0/24"},
			ip:       "192.168.1.77:45678",
			expected: true,
		},
		{
			name:     "cidr mismatch",
			rules:    []string{"192.168.1.0/24"},
			ip:       "192.168.2.190:45678",
			expected: false,
		},
		{
			name:     "multiple rules mixed",
			rules:    []string{"10.0.0.1", "192.168.1.0/24"},
			ip:       "10.0.0.1:80",
			expected: true,
		},
		{
			name:     "empty rules reject all",
			rules:    []string{},
			ip:       "127.0.0.1:80",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wl := NewWhitelist(tt.rules)
			allowed, _ := wl.IsAllowed(tt.ip)
			if allowed != tt.expected {
				t.Errorf("expected %v for IP %s with rules %v, got %v", tt.expected, tt.ip, tt.rules, allowed)
			}
		})
	}
}

func TestValidateRule(t *testing.T) {
	for _, ok := range []string{"*", "192.168.1.100", "192.168.1.0/24", "::1", " 10.0.0.1 "} {
		if err := ValidateRule(ok); err != nil {
			t.Errorf("ValidateRule(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "192.168.1", "10.0.0.0/33", "abc", "192.168.1.*"} {
		if err := ValidateRule(bad); err == nil {
			t.Errorf("ValidateRule(%q) = nil, want error", bad)
		}
	}
}

func TestDynamicLeasesRoundTrip(t *testing.T) {
	w1 := NewWhitelist([]string{"10.0.0.1"})
	w1.AddDynamic("192.168.1.5", time.Now().Add(time.Hour))
	w1.AddDynamic("192.168.1.6", time.Now().Add(-time.Second))

	w2 := NewWhitelist([]string{"10.0.0.2"})
	w2.RestoreDynamic(w1.DynamicLeases())
	if ok, _ := w2.IsAllowed("192.168.1.5:1234"); !ok {
		t.Error("restored lease not honoured")
	}
	if ok, _ := w2.IsAllowed("192.168.1.6"); ok {
		t.Error("expired lease should not be restored")
	}
}

func TestRemoveDynamic(t *testing.T) {
	w := NewWhitelist(nil)
	w.AddDynamic("10.0.0.1", time.Now().Add(time.Hour))
	w.AddDynamic("10.0.0.2", time.Now().Add(time.Hour))
	if n := w.RemoveDynamic("10.0.0.1"); n != 1 {
		t.Fatalf("removed %d, want 1", n)
	}
	if ok, _ := w.IsAllowed("10.0.0.1"); ok {
		t.Error("revoked IP still allowed")
	}
	if n := w.RemoveDynamic(""); n != 1 {
		t.Fatalf("remove all removed %d, want 1", n)
	}
	if ok, _ := w.IsAllowed("10.0.0.2"); ok {
		t.Error("IP still allowed after revoke-all")
	}
}

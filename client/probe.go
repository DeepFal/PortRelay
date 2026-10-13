package client

import (
	"fmt"
	"net"
	"time"
)

// ProbeResult holds ping/reachability info
type ProbeResult struct {
	Target    string        `json:"target"`
	Reachable bool          `json:"reachable"`
	RTT       time.Duration `json:"rtt"`
	Error     string        `json:"error,omitempty"`
}

// ProbeTarget checks if this machine can reach target
func ProbeTarget(target string, timeout time.Duration) ProbeResult {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, timeout)
	if err != nil {
		return ProbeResult{
			Target:    target,
			Reachable: false,
			RTT:       time.Since(start),
			Error:     err.Error(),
		}
	}
	defer conn.Close()

	return ProbeResult{
		Target:    target,
		Reachable: true,
		RTT:       time.Since(start),
	}
}

// FormatRTT formats duration in ms
func (pr ProbeResult) FormatRTT() string {
	return fmt.Sprintf("%.1f ms", float64(pr.RTT.Microseconds())/1000.0)
}

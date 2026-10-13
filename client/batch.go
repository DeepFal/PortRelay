package client

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// maxBatchTargets 一次批量创建的目标上限，避免误操作占满端口池
const maxBatchTargets = 64

var (
	listSeparators = regexp.MustCompile(`[\s,，、;；]+`)
	shortRange     = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.)(\d{1,3})-(\d{1,3})$`)
	fullRange      = regexp.MustCompile(`^(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})-(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})$`)
	hostnameRe     = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9._-]*[A-Za-z0-9_])?$`)
)

func splitList(s string) []string {
	var out []string
	for _, item := range listSeparators.Split(strings.TrimSpace(s), -1) {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// parsePorts accepts "22, 8080-8082"
func parsePorts(s string) ([]int, error) {
	var out []int
	for _, item := range splitList(s) {
		lo, hi := item, item
		if i := strings.Index(item, "-"); i > 0 {
			lo, hi = item[:i], item[i+1:]
		}
		a, err1 := strconv.Atoi(lo)
		b, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || a <= 0 || b > 65535 || a > b {
			return nil, fmt.Errorf("无效的端口 %q", item)
		}
		if b-a >= maxBatchTargets {
			return nil, fmt.Errorf("端口范围 %q 过大", item)
		}
		for p := a; p <= b; p++ {
			out = append(out, p)
		}
	}
	return out, nil
}

// expandIPv4Range expands "10.0.1.11-13" or "10.0.1.11-10.0.1.13"
func expandIPv4Range(item string) ([]string, bool, error) {
	var from, to net.IP
	if m := shortRange.FindStringSubmatch(item); m != nil {
		from, to = net.ParseIP(m[1]+m[2]).To4(), net.ParseIP(m[1]+m[3]).To4()
	} else if m := fullRange.FindStringSubmatch(item); m != nil {
		from, to = net.ParseIP(m[1]).To4(), net.ParseIP(m[2]).To4()
	} else {
		return nil, false, nil
	}
	if from == nil || to == nil {
		return nil, true, fmt.Errorf("无效的地址范围 %q", item)
	}
	a := uint32(from[0])<<24 | uint32(from[1])<<16 | uint32(from[2])<<8 | uint32(from[3])
	b := uint32(to[0])<<24 | uint32(to[1])<<16 | uint32(to[2])<<8 | uint32(to[3])
	if a > b {
		return nil, true, fmt.Errorf("地址范围 %q 起止颠倒", item)
	}
	if b-a >= maxBatchTargets {
		return nil, true, fmt.Errorf("地址范围 %q 过大", item)
	}
	var out []string
	for v := a; v <= b; v++ {
		out = append(out, net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String())
	}
	return out, true, nil
}

func validHost(h string) bool {
	return net.ParseIP(h) != nil || hostnameRe.MatchString(h)
}

// hostsOf expands one host item: an IPv4 range ("10.0.1.11-13") or a single IP / hostname
func hostsOf(item string) ([]string, error) {
	hosts, isRange, err := expandIPv4Range(item)
	if err != nil || isRange {
		return hosts, err
	}
	if !validHost(item) {
		return nil, fmt.Errorf("无效的主机 %q", item)
	}
	return []string{item}, nil
}

// ExpandTargets turns host and port lists into "host:port" targets, host by host.
//
// hosts 用逗号、空格或换行分隔，每一项可以是 IP、主机名、IPv4 范围（"10.0.1.11-13" 或
// "10.0.1.11-10.0.1.13"），也可以直接写成 "主机:端口"（主机和端口都可以是范围，如
// "10.0.1.11-13:22"、"10.0.1.11:8080-8082"）；ports 形如 "22, 8080-8082"，作用于没有写端口
// 的主机。结果去重并保持输入顺序。
func ExpandTargets(hosts, ports string) ([]string, error) {
	portList, err := parsePorts(ports)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	add := func(t string) error {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
		if len(out) > maxBatchTargets {
			return fmt.Errorf("一次最多添加 %d 个目标", maxBatchTargets)
		}
		return nil
	}
	items := splitList(hosts)
	if len(items) == 0 {
		return nil, fmt.Errorf("请填写至少一个目标主机")
	}
	for _, item := range items {
		itemHosts, itemPorts := item, portList
		// 自带端口的完整目标："10.0.1.11:22"、"10.0.1.11-13:22"、"[fd00::1]:22"
		if h, p, err := net.SplitHostPort(item); err == nil {
			if itemPorts, err = parsePorts(p); err != nil || len(itemPorts) == 0 {
				return nil, fmt.Errorf("无效的目标 %q", item)
			}
			itemHosts = h
		}
		hostList, err := hostsOf(itemHosts)
		if err != nil {
			return nil, err
		}
		if len(itemPorts) == 0 {
			return nil, fmt.Errorf("%q 缺少端口，请写成 主机:端口", item)
		}
		for _, h := range hostList {
			for _, p := range itemPorts {
				if err := add(net.JoinHostPort(h, strconv.Itoa(p))); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

// memberNames names batch-created tunnels after the group: "组名 (主机)" when every target
// shares one port, otherwise "组名 (主机:端口)".
func memberNames(group string, targets []string) []string {
	ports := make(map[string]bool)
	for _, t := range targets {
		if _, p, err := net.SplitHostPort(t); err == nil {
			ports[p] = true
		}
	}
	names := make([]string, len(targets))
	for i, t := range targets {
		label := t
		if len(ports) == 1 {
			if h, _, err := net.SplitHostPort(t); err == nil {
				label = h
			}
		}
		names[i] = fmt.Sprintf("%s (%s)", group, label)
	}
	return names
}

// allocatePortsLocked picks n free listen ports, preferring one contiguous block so the mapping
// is easy to read. Caller must hold cm.mu.
func (cm *ConfigManager) allocatePortsLocked(n int) ([]int, error) {
	used := make(map[int]bool)
	for _, t := range cm.cfg.Tunnels {
		used[t.ListenPort] = true
	}
	// 与单个通道的推荐顺序一致：先 33660 往上，再从端口池开头补
	var order []int
	for p := 33660; p <= PortMax; p++ {
		order = append(order, p)
	}
	for p := PortMin; p < 33660; p++ {
		order = append(order, p)
	}
	free := func(p int) bool { return p != ControlPort && !used[p] }

	for i := range order {
		ok := true
		for k := 0; k < n; k++ {
			if i+k >= len(order) || !free(order[i+k]) || (k > 0 && order[i+k] != order[i+k-1]+1) {
				ok = false
				break
			}
		}
		if ok {
			return order[i : i+n], nil
		}
	}
	var out []int
	for _, p := range order {
		if free(p) {
			out = append(out, p)
			if len(out) == n {
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("端口池剩余 %d 个空闲端口，不够分配 %d 个", len(out), n)
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/yamux"

	"github.com/DeepFal/PortRelay/core/acl"
	"github.com/DeepFal/PortRelay/core/protocol"
	"github.com/DeepFal/PortRelay/core/traffic"
)

// ActiveClient records a live client TCP connection
type ActiveClient struct {
	ID          string
	ClientIP    string
	ConnectedAt time.Time
	Conn        net.Conn
	Meter       *traffic.Meter
}

// Tunnel manages a single edge listening port
type Tunnel struct {
	Config         protocol.TunnelConfig
	listener       net.Listener
	whitelist      *acl.Whitelist
	limiter        *traffic.Limiter
	mu             sync.RWMutex
	activeConns    map[string]*ActiveClient
	totalConns     atomic.Int64
	blockedAttemps atomic.Int64
	meter          *traffic.Meter
	closed         chan struct{}
	closeOnce      sync.Once
	session        *yamux.Session
	onSecEvent     func(ev protocol.SecurityEvent)
	// onConnect 在客户端通过访问控制、连接建立时调用（用于开始授权计时）
	onConnect func(tunnelID, ip string)
}

// NewTunnel creates and binds a new tunnel listener
func NewTunnel(cfg protocol.TunnelConfig, session *yamux.Session, onSecEvent func(protocol.SecurityEvent)) (*Tunnel, error) {
	return newTunnel(cfg, session, onSecEvent, nil, nil)
}

// newTunnel binds a listener; when prev is set, its traffic counters and dynamic leases carry over
// (used when the same tunnel is re-registered or moved to another port).
func newTunnel(cfg protocol.TunnelConfig, session *yamux.Session, onSecEvent func(protocol.SecurityEvent), onConnect func(tunnelID, ip string), prev *Tunnel) (*Tunnel, error) {
	addr := fmt.Sprintf("0.0.0.0:%d", cfg.ListenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to bind port %d: %w", cfg.ListenPort, err)
	}

	t := &Tunnel{
		Config:      cfg,
		listener:    ln,
		whitelist:   acl.NewWhitelist(cfg.Whitelist),
		limiter:     traffic.NewLimiter(cfg.MaxBandwidthMB),
		activeConns: make(map[string]*ActiveClient),
		meter:       traffic.NewMeter(),
		closed:      make(chan struct{}),
		session:     session,
		onSecEvent:  onSecEvent,
		onConnect:   onConnect,
	}
	if prev != nil {
		t.meter = prev.meter
		t.totalConns.Store(prev.totalConns.Load())
		t.blockedAttemps.Store(prev.blockedAttemps.Load())
		t.whitelist.RestoreDynamic(prev.dynamicLeases())
	}

	go t.acceptLoop()
	return t, nil
}

// UpdateConfig updates whitelist, rate limiting and metadata dynamically (existing connections are kept)
func (t *Tunnel) UpdateConfig(cfg protocol.TunnelConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	dynamic := t.whitelist.DynamicLeases()
	t.Config = cfg
	t.whitelist = acl.NewWhitelist(cfg.Whitelist)
	t.whitelist.RestoreDynamic(dynamic)
	if t.limiter == nil || t.limiter.MBPerSec() != cfg.MaxBandwidthMB {
		t.limiter = traffic.NewLimiter(cfg.MaxBandwidthMB)
	}
}

// ListenPort returns the bound port
func (t *Tunnel) ListenPort() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Config.ListenPort
}

// Snapshot returns a copy of the current tunnel config
func (t *Tunnel) Snapshot() protocol.TunnelConfig {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Config
}

// ID returns the tunnel ID
func (t *Tunnel) ID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Config.ID
}

// SetDynamic replaces the temporary leases with the given set and disconnects
// active clients that are no longer permitted. Returns how many were disconnected.
func (t *Tunnel) SetDynamic(leases map[string]time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.whitelist.ReplaceDynamic(leases)
	kicked := 0
	for id, ac := range t.activeConns {
		if ok, _ := t.whitelist.IsAllowed(ac.ClientIP); !ok {
			_ = ac.Conn.Close()
			delete(t.activeConns, id)
			kicked++
		}
	}
	return kicked
}

// GroupID returns the tunnel group this tunnel belongs to ("" when none)
func (t *Tunnel) GroupID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Config.GroupID
}

// Name returns the tunnel display name
func (t *Tunnel) Name() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Config.Name
}

// AuthCode returns the web auth code
func (t *Tunnel) AuthCode() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Config.AuthCode
}

// AddDynamicWhitelist grants temporary IP access
func (t *Tunnel) AddDynamicWhitelist(ip string, exp time.Time) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.whitelist.AddDynamic(ip, exp)
}

// GetDynamicWhitelist returns active granted IPs
func (t *Tunnel) GetDynamicWhitelist() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.whitelist.GetDynamic()
}

// RevokeDynamic removes temporary leases (all when ip is empty) and disconnects
// active clients that are no longer permitted by the remaining rules.
func (t *Tunnel) RevokeDynamic(ip string) (revoked, kicked int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	revoked = t.whitelist.RemoveDynamic(ip)
	if revoked == 0 {
		return 0, 0
	}
	for id, ac := range t.activeConns {
		if ok, _ := t.whitelist.IsAllowed(ac.ClientIP); !ok {
			_ = ac.Conn.Close()
			delete(t.activeConns, id)
			kicked++
		}
	}
	return revoked, kicked
}

// EnforceAccess disconnects active clients that are no longer permitted — e.g. their
// temporary lease expired or the whitelist changed. Returns the disconnected client addresses.
func (t *Tunnel) EnforceAccess() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var kicked []string
	for id, ac := range t.activeConns {
		if ok, _ := t.whitelist.IsAllowed(ac.ClientIP); !ok {
			_ = ac.Conn.Close()
			delete(t.activeConns, id)
			kicked = append(kicked, ac.ClientIP)
		}
	}
	return kicked
}

func (t *Tunnel) dynamicLeases() map[string]time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.whitelist.DynamicLeases()
}

func (t *Tunnel) acceptLoop() {
	var backoff time.Duration
	for {
		clientConn, err := t.listener.Accept()
		if err != nil {
			select {
			case <-t.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// 临时错误（如文件句柄耗尽）退避后继续，不能让监听永久退出
			if backoff == 0 {
				backoff = 5 * time.Millisecond
			} else if backoff *= 2; backoff > time.Second {
				backoff = time.Second
			}
			log.Printf("[Tunnel %d] accept error: %v; retrying in %v", t.ListenPort(), err, backoff)
			select {
			case <-t.closed:
				return
			case <-time.After(backoff):
			}
			continue
		}
		backoff = 0

		go t.handleClient(clientConn)
	}
}

func (t *Tunnel) handleClient(clientConn net.Conn) {
	remoteAddr := clientConn.RemoteAddr().String()

	// 1. Check IP Whitelist immediately at the edge
	t.mu.RLock()
	allowed, reason := t.whitelist.IsAllowed(remoteAddr)
	tunnelID := t.Config.ID
	listenPort := t.Config.ListenPort
	t.mu.RUnlock()

	if !allowed {
		t.blockedAttemps.Add(1)
		_ = clientConn.Close()

		if t.onSecEvent != nil {
			t.onSecEvent(protocol.SecurityEvent{
				ID:        uuid.New().String(),
				TunnelID:  tunnelID,
				Port:      listenPort,
				ClientIP:  remoteAddr,
				Timestamp: time.Now(),
				Reason:    reason,
			})
		}
		log.Printf("[Tunnel %d] BLOCKED connection from %s (%s)", listenPort, remoteAddr, reason)
		return
	}

	t.totalConns.Add(1)
	connID := uuid.New().String()
	if t.onConnect != nil {
		host := remoteAddr
		if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
			host = h
		}
		t.onConnect(tunnelID, host)
	}

	// 2. Open Yamux stream to Mac agent
	stream, err := t.session.OpenStream()
	if err != nil {
		log.Printf("[Tunnel %d] failed to open stream to agent: %v", listenPort, err)
		_ = clientConn.Close()
		return
	}

	// 3. Send handshake line
	handshake := protocol.StreamHandshake{
		TunnelID: tunnelID,
		ConnID:   connID,
		ClientIP: remoteAddr,
	}
	data, _ := json.Marshal(handshake)
	data = append(data, '\n')
	if _, err := stream.Write(data); err != nil {
		_ = stream.Close()
		_ = clientConn.Close()
		return
	}

	// 4. Track active connection
	connMeter := traffic.NewMeter()
	wrappedClientConn := traffic.WrapConn(clientConn, connMeter)

	ac := &ActiveClient{
		ID:          connID,
		ClientIP:    remoteAddr,
		ConnectedAt: time.Now(),
		Conn:        clientConn,
		Meter:       connMeter,
	}

	t.mu.Lock()
	t.activeConns[connID] = ac
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.activeConns, connID)
		t.mu.Unlock()
		_ = wrappedClientConn.Close()
		_ = stream.Close()
	}()

	// 5. Bidirectional copy with optional QoS rate limiting
	done := make(chan struct{}, 2)
	go func() {
		buf := make([]byte, 32*1024)
		for {
			nr, er := wrappedClientConn.Read(buf)
			if nr > 0 {
				t.meter.AddIn(int64(nr))
				t.mu.RLock()
				lim := t.limiter
				t.mu.RUnlock()
				if lim != nil {
					lim.Allow(nr)
				}
				nw, ew := stream.Write(buf[0:nr])
				if ew != nil || nr != nw {
					break
				}
			}
			if er != nil {
				break
			}
		}
		_ = stream.Close()
		_ = wrappedClientConn.Close()
		done <- struct{}{}
	}()

	go func() {
		buf := make([]byte, 32*1024)
		for {
			nr, er := stream.Read(buf)
			if nr > 0 {
				t.meter.AddOut(int64(nr))
				t.mu.RLock()
				lim := t.limiter
				t.mu.RUnlock()
				if lim != nil {
					lim.Allow(nr)
				}
				nw, ew := wrappedClientConn.Write(buf[0:nr])
				if ew != nil || nr != nw {
					break
				}
			}
			if er != nil {
				break
			}
		}
		_ = stream.Close()
		_ = wrappedClientConn.Close()
		done <- struct{}{}
	}()

	<-done
}

// KillConn closes a specific connection by ID
func (t *Tunnel) KillConn(connID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ac, ok := t.activeConns[connID]; ok {
		_ = ac.Conn.Close()
		delete(t.activeConns, connID)
		return true
	}
	return false
}

// Close shuts down the listener and disconnects all clients
func (t *Tunnel) Close() {
	t.closeOnce.Do(func() {
		close(t.closed)
		_ = t.listener.Close()

		t.mu.Lock()
		for _, ac := range t.activeConns {
			_ = ac.Conn.Close()
		}
		t.activeConns = make(map[string]*ActiveClient)
		t.mu.Unlock()
	})
}

// Tick updates metrics
func (t *Tunnel) Tick() {
	t.meter.Tick()
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, ac := range t.activeConns {
		ac.Meter.Tick()
	}
}

// Metrics returns the current metrics snapshot
func (t *Tunnel) Metrics() (protocol.TunnelMetrics, []protocol.ActiveConnInfo) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	bIn, bOut, rIn, rOut := t.meter.Stats()
	tm := protocol.TunnelMetrics{
		TunnelID:         t.Config.ID,
		ListenPort:       t.Config.ListenPort,
		ActiveConns:      len(t.activeConns),
		TotalConns:       t.totalConns.Load(),
		BytesIn:          bIn,
		BytesOut:         bOut,
		RateIn:           rIn,
		RateOut:          rOut,
		BlockedAttemps:   t.blockedAttemps.Load(),
		Status:           "online",
		DynamicWhitelist: t.whitelist.GetDynamic(),
	}
	for ip, exp := range t.whitelist.DynamicLeases() {
		tm.DynamicLeases = append(tm.DynamicLeases, protocol.DynamicLease{IP: ip, ExpiresAt: exp})
	}
	sort.Slice(tm.DynamicLeases, func(i, j int) bool {
		return tm.DynamicLeases[i].ExpiresAt.Before(tm.DynamicLeases[j].ExpiresAt)
	})

	conns := make([]protocol.ActiveConnInfo, 0, len(t.activeConns))
	for _, ac := range t.activeConns {
		cbIn, cbOut, crIn, crOut := ac.Meter.Stats()
		conns = append(conns, protocol.ActiveConnInfo{
			ConnID:      ac.ID,
			TunnelID:    t.Config.ID,
			ClientIP:    ac.ClientIP,
			ConnectedAt: ac.ConnectedAt,
			BytesIn:     cbIn,
			BytesOut:    cbOut,
			RateIn:      crIn,
			RateOut:     crOut,
		})
	}
	return tm, conns
}

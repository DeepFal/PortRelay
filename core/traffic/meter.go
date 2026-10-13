package traffic

import (
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Meter tracks bandwidth and total bytes
type Meter struct {
	bytesIn      atomic.Int64 // Read from client (upload)
	bytesOut     atomic.Int64 // Written to client (download)
	lastBytesIn  int64
	lastBytesOut int64
	rateIn       atomic.Int64 // Bytes per second
	rateOut      atomic.Int64 // Bytes per second
	lastTick     time.Time
}

// NewMeter creates a new meter
func NewMeter() *Meter {
	return &Meter{
		lastTick: time.Now(),
	}
}

// AddIn adds incoming bytes
func (m *Meter) AddIn(n int64) {
	m.bytesIn.Add(n)
}

// AddOut adds outgoing bytes
func (m *Meter) AddOut(n int64) {
	m.bytesOut.Add(n)
}

// Tick calculates current transfer rate
func (m *Meter) Tick() {
	now := time.Now()
	elapsed := now.Sub(m.lastTick).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}

	currIn := m.bytesIn.Load()
	currOut := m.bytesOut.Load()

	diffIn := currIn - m.lastBytesIn
	diffOut := currOut - m.lastBytesOut

	m.rateIn.Store(int64(float64(diffIn) / elapsed))
	m.rateOut.Store(int64(float64(diffOut) / elapsed))

	m.lastBytesIn = currIn
	m.lastBytesOut = currOut
	m.lastTick = now
}

// Stats returns snapshot values
func (m *Meter) Stats() (bytesIn, bytesOut, rateIn, rateOut int64) {
	return m.bytesIn.Load(), m.bytesOut.Load(), m.rateIn.Load(), m.rateOut.Load()
}

// Conn wraps a net.Conn with traffic metering
type Conn struct {
	net.Conn
	meter *Meter
}

// WrapConn wraps a net.Conn
func WrapConn(c net.Conn, meter *Meter) *Conn {
	return &Conn{
		Conn:  c,
		meter: meter,
	}
}

func (c *Conn) Read(b []byte) (n int, err error) {
	n, err = c.Conn.Read(b)
	if n > 0 {
		c.meter.AddIn(int64(n))
	}
	return
}

func (c *Conn) Write(b []byte) (n int, err error) {
	n, err = c.Conn.Write(b)
	if n > 0 {
		c.meter.AddOut(int64(n))
	}
	return
}

// Pipe copies data bidirectionally between two ReadWriteClosers with buffer pooling
func Pipe(c1, c2 io.ReadWriteCloser) {
	PipeWithLimiter(c1, c2, nil)
}

// Limiter implements a token-bucket rate limiter for bandwidth QoS.
// 令牌可以透支，调用方在锁外按欠额休眠，多个连接共享同一个桶时不会互相串行阻塞。
type Limiter struct {
	mbPerSec   float64
	rate       float64 // bytes per second
	capacity   float64 // burst capacity
	tokens     float64
	lastUpdate time.Time
	mu         sync.Mutex
}

// NewLimiter creates a limiter with MB/s target
func NewLimiter(mbPerSec float64) *Limiter {
	if mbPerSec <= 0 {
		return nil
	}
	bytesPerSec := mbPerSec * 1024 * 1024
	return &Limiter{
		mbPerSec:   mbPerSec,
		rate:       bytesPerSec,
		capacity:   bytesPerSec * 2,
		tokens:     bytesPerSec,
		lastUpdate: time.Now(),
	}
}

// MBPerSec returns the configured limit (0 for a nil limiter)
func (l *Limiter) MBPerSec() float64 {
	if l == nil {
		return 0
	}
	return l.mbPerSec
}

// reserve takes n tokens and returns how long the caller must wait before proceeding
func (l *Limiter) reserve(n int) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if elapsed := now.Sub(l.lastUpdate).Seconds(); elapsed > 0 {
		l.tokens += elapsed * l.rate
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
		l.lastUpdate = now
	}

	l.tokens -= float64(n)
	if l.tokens >= 0 {
		return 0
	}
	return time.Duration(-l.tokens / l.rate * float64(time.Second))
}

// Allow limits byte throughput
func (l *Limiter) Allow(n int) {
	if l == nil || n <= 0 {
		return
	}
	if wait := l.reserve(n); wait > 0 {
		time.Sleep(wait)
	}
}

// LimitedReader wraps an io.Reader with a Limiter
type LimitedReader struct {
	R       io.Reader
	Limiter *Limiter
}

func (lr *LimitedReader) Read(p []byte) (n int, err error) {
	n, err = lr.R.Read(p)
	if n > 0 && lr.Limiter != nil {
		lr.Limiter.Allow(n)
	}
	return
}

// PipeWithLimiter copies data bidirectionally between two ReadWriteClosers with optional rate limiting
func PipeWithLimiter(c1, c2 io.ReadWriteCloser, limiter *Limiter) {
	done := make(chan struct{}, 2)

	go func() {
		buf := make([]byte, 32*1024)
		var r io.Reader = c2
		if limiter != nil {
			r = &LimitedReader{R: c2, Limiter: limiter}
		}
		_, _ = io.CopyBuffer(c1, r, buf)
		_ = c1.Close()
		_ = c2.Close()
		done <- struct{}{}
	}()

	go func() {
		buf := make([]byte, 32*1024)
		var r io.Reader = c1
		if limiter != nil {
			r = &LimitedReader{R: c1, Limiter: limiter}
		}
		_, _ = io.CopyBuffer(c2, r, buf)
		_ = c1.Close()
		_ = c2.Close()
		done <- struct{}{}
	}()

	<-done
	<-done
}

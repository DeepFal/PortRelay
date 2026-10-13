package client

import (
	"sync"
	"time"
)

const (
	rateStep    = 5 * time.Second
	ratePoints  = 120 // 10 分钟
	totalSeries = "__total__"
)

// RateSeries is a fixed-length rate history (oldest first). The last point is the
// current, still-accumulating bucket so the chart moves every second.
type RateSeries struct {
	End  int64   `json:"end"`  // 最后一个点所在 5 秒桶的起始时间（Unix 秒）
	Step int     `json:"step"` // 每个点的秒数
	Up   []int64 `json:"up"`   // 上行 bytes/s（客户端 → 目标）
	Down []int64 `json:"down"` // 下行 bytes/s（目标 → 客户端）
}

type rateBucket struct {
	start    time.Time
	up, down int64
}

type rateHistory struct {
	done             []rateBucket // completed buckets, oldest first, at most ratePoints
	curStart         time.Time
	curUp, curDown   int64
	curSamples       int64
	lastSampleBucket time.Time
}

func (h *rateHistory) add(now time.Time, up, down int64) {
	bs := now.Truncate(rateStep)
	if h.curStart.IsZero() {
		h.curStart = bs
	}
	if bs.After(h.curStart) {
		if h.curSamples > 0 {
			h.done = append(h.done, rateBucket{h.curStart, h.curUp / h.curSamples, h.curDown / h.curSamples})
		}
		if len(h.done) > ratePoints {
			h.done = h.done[len(h.done)-ratePoints:]
		}
		h.curStart, h.curUp, h.curDown, h.curSamples = bs, 0, 0, 0
	}
	h.curUp += up
	h.curDown += down
	h.curSamples++
}

// series renders the last ratePoints buckets ending at now's bucket; missing buckets are zero
func (h *rateHistory) series(now time.Time) RateSeries {
	end := now.Truncate(rateStep)
	s := RateSeries{End: end.Unix(), Step: int(rateStep / time.Second), Up: make([]int64, ratePoints), Down: make([]int64, ratePoints)}
	first := end.Add(-time.Duration(ratePoints-1) * rateStep)
	put := func(start time.Time, up, down int64) {
		if start.Before(first) || start.After(end) {
			return
		}
		i := int(start.Sub(first) / rateStep)
		s.Up[i], s.Down[i] = up, down
	}
	for _, b := range h.done {
		put(b.start, b.up, b.down)
	}
	if h.curSamples > 0 {
		put(h.curStart, h.curUp/h.curSamples, h.curDown/h.curSamples)
	}
	return s
}

// rateRecorder keeps per-tunnel and total rate histories in memory (近 10 分钟，不落盘)
type rateRecorder struct {
	mu   sync.Mutex
	hist map[string]*rateHistory
}

func newRateRecorder() *rateRecorder {
	return &rateRecorder{hist: make(map[string]*rateHistory)}
}

// record adds one sample per tunnel (rates in bytes/s) plus the total
func (r *rateRecorder) record(now time.Time, rates map[string][2]int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var tu, td int64
	for id, v := range rates {
		h := r.hist[id]
		if h == nil {
			h = &rateHistory{}
			r.hist[id] = h
		}
		h.add(now, v[0], v[1])
		tu += v[0]
		td += v[1]
	}
	t := r.hist[totalSeries]
	if t == nil {
		t = &rateHistory{}
		r.hist[totalSeries] = t
	}
	t.add(now, tu, td)
}

func (r *rateRecorder) series(id string, now time.Time) RateSeries {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h := r.hist[id]; h != nil {
		return h.series(now)
	}
	return (&rateHistory{}).series(now)
}

// forget drops the history of tunnels that are no longer enabled
func (r *rateRecorder) retain(enabled map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.hist {
		if id != totalSeries && !enabled[id] {
			delete(r.hist, id)
		}
	}
}

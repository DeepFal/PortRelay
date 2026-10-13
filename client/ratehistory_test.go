package client

import (
	"testing"
	"time"
)

func TestRateHistoryBucketsAndWindow(t *testing.T) {
	r := newRateRecorder()
	base := time.Unix(1_700_000_000, 0).Truncate(rateStep)

	// 同一个 5 秒桶内的多个采样取平均
	r.record(base, map[string][2]int64{"a": {100, 1000}})
	r.record(base.Add(1*time.Second), map[string][2]int64{"a": {300, 3000}})
	// 跳过 3 个桶（中间无数据，应为 0）
	r.record(base.Add(20*time.Second), map[string][2]int64{"a": {50, 500}})

	s := r.series("a", base.Add(21*time.Second))
	if len(s.Up) != ratePoints || len(s.Down) != ratePoints || s.Step != 5 {
		t.Fatalf("series must be fixed length %d with step 5: %d/%d step %d", ratePoints, len(s.Up), len(s.Down), s.Step)
	}
	last := ratePoints - 1
	if s.Up[last] != 50 || s.Down[last] != 500 {
		t.Fatalf("last point should be the current bucket: %d/%d", s.Up[last], s.Down[last])
	}
	if s.Up[last-4] != 200 || s.Down[last-4] != 2000 {
		t.Fatalf("first bucket should average samples: %d/%d", s.Up[last-4], s.Down[last-4])
	}
	for i := last - 3; i < last; i++ {
		if s.Up[i] != 0 || s.Down[i] != 0 {
			t.Fatalf("gap bucket %d should be zero: %d/%d", i, s.Up[i], s.Down[i])
		}
	}

	// 合计曲线
	total := r.series(totalSeries, base.Add(21*time.Second))
	if total.Down[last] != 500 {
		t.Fatalf("total should sum tunnels: %d", total.Down[last])
	}

	// 11 分钟后旧数据滑出窗口
	later := base.Add(11 * time.Minute)
	if s2 := r.series("a", later); s2.Up[0] != 0 || s2.Down[last] != 0 {
		t.Fatalf("old data should slide out of the 10 minute window: first=%d last=%d", s2.Up[0], s2.Down[last])
	}

	// 关闭的通道清除曲线
	r.retain(map[string]bool{})
	if s3 := r.series("a", base.Add(21*time.Second)); s3.Down[last] != 0 {
		t.Fatal("history of a disabled tunnel should be dropped")
	}
}

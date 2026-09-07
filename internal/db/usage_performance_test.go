package db

import (
	"math/rand"
	"sort"
	"testing"
	"time"
)

func TestUsageSQLiteTimestampFormats(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"2026-09-07 12:34:56", "2026-09-07T12:34:56Z"},
		{"2026-09-07 12:34:56.123456789", "2026-09-07T12:34:56.123456789Z"},
		{"2026-09-07T12:34:56.123456789Z", "2026-09-07T12:34:56.123456789Z"},
		{"2026-09-07 12:34:56.123456789+02:00", "2026-09-07T10:34:56.123456789Z"},
		{"2026-09-07T12:34:56-03:30", "2026-09-07T16:04:56Z"},
		{"2026-09-07 12:34:56.123456789 +0200 CEST", "2026-09-07T10:34:56.123456789Z"},
		{"2026-09-07 12:34:56 +0000 UTC m=+123.4", "2026-09-07T12:34:56Z"},
		{"2026-09-07T12:34:56.123", "2026-09-07T12:34:56.123Z"},
		{"2026-09-07 12:34", "2026-09-07T12:34:00Z"},
		{"2026-09-07T12:34", "2026-09-07T12:34:00Z"},
		{"2026-09-07", "2026-09-07T00:00:00Z"},
		{"", ""}, {"2026-02-30 12:34:56", ""}, {"2026-09-07 25:00:00", ""},
		{"2026-09-07T12:34:56Z junk", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := parseSQLiteTime(tc.input)
			if tc.want == "" {
				if ok {
					t.Fatalf("accepted invalid timestamp %q", tc.input)
				}
				return
			}
			if !ok || got.Format(time.RFC3339Nano) != tc.want || got.Location() != time.UTC {
				t.Fatalf("parse = %v, %v; want %s in UTC", got, ok, tc.want)
			}
		})
	}
}

func TestUsageEndHeapInterleavedIntervals(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	var heap usageEndHeap
	var want []int64
	for range 10000 {
		if len(want) == 0 || rng.Intn(2) == 0 {
			value := rng.Int63n(1000) - 500
			heap.push(value)
			want = append(want, value)
			sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		} else {
			if heap[0] != want[0] {
				t.Fatalf("next end = %d, want %d", heap[0], want[0])
			}
			heap.pop()
			want = want[1:]
		}
		if len(heap) != len(want) {
			t.Fatal("lost an interval")
		}
	}
	for _, value := range want {
		if heap[0] != value {
			t.Fatalf("drain = %d, want %d", heap[0], value)
		}
		heap.pop()
	}
}

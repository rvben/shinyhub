package data

import "testing"

func TestProjectedSize(t *testing.T) {
	cases := []struct {
		name     string
		used     int64
		existing int64
		incoming int64
		want     int64
	}{
		{"new file", 900_000_000, 0, 200_000_000, 1_100_000_000},
		{"overwrite shrink", 900_000_000, 100_000_000, 50_000_000, 850_000_000},
		{"overwrite grow", 900_000_000, 100_000_000, 200_000_000, 1_000_000_000},
		{"overwrite same", 900_000_000, 100_000_000, 100_000_000, 900_000_000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ProjectedSize(c.used, c.existing, c.incoming); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

func TestQuotaCheck(t *testing.T) {
	quota := int64(1024 * 1024 * 1024) // 1 GiB
	if err := QuotaCheck(900_000_000, 0, 200_000_000, quota); err == nil {
		t.Error("want quota exceeded")
	}
	if err := QuotaCheck(900_000_000, 100_000_000, 50_000_000, quota); err != nil {
		t.Errorf("want OK, got %v", err)
	}
	if err := QuotaCheck(900_000_000, 100_000_000, 200_000_000, quota); err != nil {
		t.Errorf("want OK at exact cap, got %v", err)
	}
	if err := QuotaCheck(900_000_000, 0, 200_000_000, 0); err != nil {
		t.Errorf("quota=0 means unlimited, got %v", err)
	}
}

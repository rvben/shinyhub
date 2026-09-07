package main

import (
	"os"
	"testing"
)

func TestTargetResources(t *testing.T) {
	files := map[string]string{
		"/sys/fs/cgroup/cpu.stat": "usage_usec 1234\nnr_periods 100\nnr_throttled 9\nthrottled_usec 700",
		"/sys/fs/cgroup/cpu.max":  "200000 100000",
		"/proc/loadavg":           "1.50 2.00 3.00 2/123 45",
		"/proc/stat":              "cpu  100 0 20 800 30 2 3 5 0 0\ncpu0 0\ncpu1 0\nintr 123",
	}
	read := func(path string) ([]byte, error) {
		v, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(v), nil
	}
	got := targetResources(read)
	if err := got.validate(); err != nil {
		t.Fatal(err)
	}
	if got.HostCPUs != 2 || got.HostTicks[3] != 800 || got.HostLoad[0] != 1.5 || got.CPUStat["throttled_usec"] != 700 {
		t.Fatalf("snapshot: %+v", got)
	}
	delete(files, "/sys/fs/cgroup/cpu.stat")
	if err := targetResources(read).validate(); err == nil {
		t.Fatal("missing throttling data was accepted")
	}
	files["/sys/fs/cgroup/cpu.stat"] = "usage_usec invalid"
	if err := targetResources(read).validate(); err == nil {
		t.Fatal("malformed CPU data was accepted")
	}
}

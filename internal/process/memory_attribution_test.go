package process

import (
	"strings"
	"testing"
)

func TestParseMemoryAttribution(t *testing.T) {
	in := `00400000-00452000 r-xp 00000000 00:00 0 [rollup]
Rss:                 240 kB
Pss:                 180 kB
Private_Clean:        20 kB
Private_Dirty:        70 kB
Private_Hugetlb:       4 kB
SwapPss:               3 kB
`
	got, err := parseMemoryAttribution(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got.PSS != 180*1024 || got.USS != 94*1024 || got.SwapPSS != 3*1024 {
		t.Fatalf("attribution = %+v", got)
	}
}

func TestParseMemoryAttributionRequiresPSS(t *testing.T) {
	_, err := parseMemoryAttribution(strings.NewReader("Rss: 12 kB\n"))
	if err == nil {
		t.Fatal("expected missing Pss error")
	}
}

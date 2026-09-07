package process

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/logstream"
)

func TestSnapshotTailMatchesRetainedRecords(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	contents := []string{"", "\n", "\n\n", "a\r\n\nlast\r", "partial", strings.Repeat("x", 32767) + "\r\nlast\n"}
	for range 25 {
		var content strings.Builder
		for range 1 + rng.Intn(12) {
			content.WriteString(strings.Repeat("x", rng.Intn(65536)))
			content.WriteString([]string{"\n", "\r\n", ""}[rng.Intn(3)])
		}
		contents = append(contents, content.String())
	}
	for i, content := range contents {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			reader := tailReaderWith(t, content)
			all := logstream.RecordsFromBytes([]byte(content), 0)
			for _, n := range []int{-1, 0, 1, 2, 5, 100} {
				got, cursor, err := reader.SnapshotTail(n)
				want := all
				if n <= 0 {
					want = nil
				} else if len(want) > n {
					want = want[len(want)-n:]
				}
				if err != nil || cursor != int64(len(content)) || !reflect.DeepEqual(got, want) {
					t.Fatalf("tail(%d): records=%d want=%d cursor=%d err=%v", n, len(got), len(want), cursor, err)
				}
			}
		})
	}
}

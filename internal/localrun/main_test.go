package localrun

import (
	"os"
	"testing"

	"github.com/rvben/shinyhub/internal/testhome"
)

// TestMain gives the suite a private home directory: workspaces default under
// os.UserCacheDir and every data lock is registered there, so without the
// redirect the tests write into the developer's real cache.
func TestMain(m *testing.M) {
	os.Exit(testhome.Run(m))
}

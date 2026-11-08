package localrun

import (
	"testing"

	"github.com/rvben/shinyhub/internal/testhome"
)

// Local runs register their data locks under os.UserCacheDir and default
// their workspace there too, so a test binary that is not redirected writes
// into the developer's real cache directory. The package TestMain must keep
// every lookup inside a private home.
func TestUserDirectoriesAreHermetic(t *testing.T) {
	testhome.Require(t)
}

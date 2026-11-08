package cli

import (
	"testing"

	"github.com/rvben/shinyhub/internal/testhome"
)

// The run, dev and fleet dev commands drive localrun, which registers data
// locks under os.UserCacheDir, and the credentials store defaults under the
// home directory. The package TestMain must keep every lookup inside a
// private home so the suite cannot touch the developer's real profile.
func TestUserDirectoriesAreHermetic(t *testing.T) {
	testhome.Require(t)
}

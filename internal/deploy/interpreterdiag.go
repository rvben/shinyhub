package deploy

import (
	"fmt"

	"github.com/rvben/shinyhub/internal/deployfail"
)

// interpreterResolutionHint annotates a failed build step whose output carries
// uv's interpreter-provisioning signature (a blocked python-build-standalone
// download, or "No interpreter found" for the project's requires-python) with
// the server-level knobs that resolve it. Unlike indexResolutionHint this is
// about obtaining the Python *interpreter*, not resolving *dependencies*: on a
// host whose egress proxy blocks GitHub, uv cannot download a managed CPython
// and the fix is to point it at a mirror or fall back to a system interpreter,
// none of which the raw uv error mentions.
//
// It only adds text; the machine-readable failure_kind is assigned separately
// by deployfail.Classify, which keys on the same signature via
// deployfail.MentionsInterpreterProvisioningFailure. Errors without the
// signature, and nil, pass through unchanged.
func interpreterResolutionHint(out []byte, err error) error {
	if err == nil || !deployfail.MentionsInterpreterProvisioningFailure(string(out)) {
		return err
	}
	return fmt.Errorf("%w (uv could not obtain a Python interpreter for this app; on a host without egress to GitHub's python-build-standalone releases, set build.python_preference: only-system to use a preinstalled interpreter, or build.python_install_mirror to an internal mirror, in the server config - see docs/environment.md)", err)
}

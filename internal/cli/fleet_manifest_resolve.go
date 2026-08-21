package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const (
	// defaultFleetManifest is the manifest a fleet command reads (and `fleet
	// init` writes) when -f is omitted. The bundle manifest keeps the shinyhub
	// prefix because it lives among foreign files in a developer's app repo; a
	// fleet manifest lives in a repo that exists for the fleet and is invoked as
	// `shinyhub fleet ...`, so the subcommand supplies the namespace and the
	// filename does not have to.
	defaultFleetManifest = "fleet.toml"

	// legacyFleetManifest is the previous default name. It is still read as a
	// backward-compatible fallback when fleet.toml is absent, so existing
	// repositories keep working without renaming their manifest.
	legacyFleetManifest = "shinyhub-fleet.toml"
)

// chooseFleetManifest decides which manifest path a fleet command reads. An
// explicit -f wins outright. With -f omitted, fleet.toml is preferred; the
// legacy shinyhub-fleet.toml is used (usedLegacy=true) only when fleet.toml does
// not exist but the legacy file does. When neither exists the modern name is
// returned so the resulting not-found error names fleet.toml.
func chooseFleetManifest(explicit bool, flagValue string, exists func(string) bool) (path string, usedLegacy bool) {
	if explicit {
		return flagValue, false
	}
	path, _, usedLegacy = chooseFleetManifestInDir("", exists)
	return path, usedLegacy
}

// chooseFleetManifestInDir applies the same default-name precedence within a
// particular directory. The found result distinguishes "use the modern name
// in an eventual not-found error" from a manifest that actually exists. Plain
// command omission discovery shares this function with fleet commands so the
// two paths cannot disagree when both modern and legacy files are present.
func chooseFleetManifestInDir(dir string, exists func(string) bool) (path string, found, usedLegacy bool) {
	modern := filepath.Join(dir, defaultFleetManifest)
	if exists(modern) {
		return modern, true, false
	}
	legacy := filepath.Join(dir, legacyFleetManifest)
	if exists(legacy) {
		return legacy, true, true
	}
	return modern, false, false
}

// resolveFleetManifest applies chooseFleetManifest against the real filesystem
// and writes a one-line deprecation note to w when it falls back to the legacy
// name, so an operator on the old filename is steered toward fleet.toml without
// being broken. The -f value is honored verbatim when the operator set it
// explicitly (cmd reports that via the cobra Changed marker).
func resolveFleetManifest(cmd *cobra.Command, flagValue string, w io.Writer) string {
	path, usedLegacy := chooseFleetManifest(cmd.Flags().Changed("file"), flagValue, fileExists)
	if usedLegacy {
		fmt.Fprintf(w, "note: reading %s (deprecated name); rename it to %s\n",
			legacyFleetManifest, defaultFleetManifest)
	}
	return path
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

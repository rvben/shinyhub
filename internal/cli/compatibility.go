package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/rvben/shinyhub/internal/protocol"
)

type compatibilityLevel string

const (
	compatibilityCompatible   compatibilityLevel = "compatible"
	compatibilityWarning      compatibilityLevel = "warning"
	compatibilityIncompatible compatibilityLevel = "incompatible"
)

type compatibilityDiagnosis struct {
	Level  compatibilityLevel
	Detail string
	Fix    string
}

type releaseVersion struct {
	major int
	minor int
	patch int
}

func diagnoseCompatibility(cliVersion string, info serverInfo) compatibilityDiagnosis {
	cliLabel := displayVersion(cliVersion)
	serverLabel := displayVersion(info.Version)
	if info.ProtocolVersion > protocol.CurrentVersion {
		return compatibilityDiagnosis{
			Level: compatibilityIncompatible,
			Detail: fmt.Sprintf("server API protocol %d is newer than this CLI supports (protocol %d; CLI %s, server %s)",
				info.ProtocolVersion, protocol.CurrentVersion, cliLabel, serverLabel),
			Fix: upgradeCLICompatibilityFix(),
		}
	}

	cliRelease, cliKnown := parseReleaseVersion(cliVersion)
	serverRelease, serverKnown := parseReleaseVersion(info.Version)
	if strings.TrimSpace(info.Version) == "" {
		return compatibilityDiagnosis{
			Level:  compatibilityWarning,
			Detail: "the server does not report its version; ShinyHub will negotiate features individually",
			Fix:    "Upgrade the server when practical so compatibility can be verified before commands run.",
		}
	}

	protocolDetail := "legacy capability negotiation"
	if info.ProtocolVersion > 0 {
		protocolDetail = fmt.Sprintf("API protocol %d", info.ProtocolVersion)
	}
	if cliVersion == info.Version || (cliKnown && serverKnown && sameReleaseLine(cliRelease, serverRelease)) {
		return compatibilityDiagnosis{
			Level:  compatibilityCompatible,
			Detail: fmt.Sprintf("CLI %s and server %s are compatible (%s)", cliLabel, serverLabel, protocolDetail),
		}
	}

	if cliKnown && serverKnown {
		if compareRelease(cliRelease, serverRelease) < 0 {
			return compatibilityDiagnosis{
				Level: compatibilityWarning,
				Detail: fmt.Sprintf("CLI %s is older than server %s; supported commands remain available through %s, but newer features may be missing",
					cliLabel, serverLabel, protocolDetail),
				Fix: upgradeCLICompatibilityFix(),
			}
		}
		return compatibilityDiagnosis{
			Level: compatibilityWarning,
			Detail: fmt.Sprintf("server %s is older than CLI %s; supported commands remain capability-gated, but newer features may be unavailable",
				serverLabel, cliLabel),
			Fix: "Upgrade the ShinyHub server; until then, use the commands and capabilities it advertises.",
		}
	}

	if info.ProtocolVersion == protocol.CurrentVersion {
		return compatibilityDiagnosis{
			Level:  compatibilityCompatible,
			Detail: fmt.Sprintf("CLI %s and server %s share API protocol %d", cliLabel, serverLabel, protocol.CurrentVersion),
		}
	}
	return compatibilityDiagnosis{
		Level: compatibilityWarning,
		Detail: fmt.Sprintf("CLI %s and server %s cannot be compared by release; the server uses legacy capability negotiation",
			cliLabel, serverLabel),
		Fix: "Upgrade the CLI and server to current documented releases, then run `shinyhub doctor --remote` again.",
	}
}

func parseReleaseVersion(raw string) (releaseVersion, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if cut := strings.IndexAny(value, "-+"); cut >= 0 {
		value = value[:cut]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return releaseVersion{}, false
	}
	numbers := [3]int{}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return releaseVersion{}, false
		}
		numbers[i] = n
	}
	return releaseVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, true
}

// sameReleaseLine follows SemVer's stability boundary: before 1.0, a minor
// release may break compatibility; after 1.0, that boundary is the major.
func sameReleaseLine(a, b releaseVersion) bool {
	if a.major == 0 || b.major == 0 {
		return a.major == b.major && a.minor == b.minor
	}
	return a.major == b.major
}

func compareRelease(a, b releaseVersion) int {
	av := [3]int{a.major, a.minor, a.patch}
	bv := [3]int{b.major, b.minor, b.patch}
	for i := range av {
		if av[i] < bv[i] {
			return -1
		}
		if av[i] > bv[i] {
			return 1
		}
	}
	return 0
}

func upgradeCLICompatibilityFix() string {
	return "Upgrade the CLI with `uv tool upgrade shinyhub`, or use the same package manager that installed it."
}

func compatibilityError(d compatibilityDiagnosis) error {
	return &ExitCodeError{Code: 1, Kind: KindValidation, Err: &hintedMsgError{msg: d.Detail, hint: d.Fix}}
}

func reportConnectCompatibility(out io.Writer, d compatibilityDiagnosis) error {
	if d.Level == compatibilityIncompatible {
		return compatibilityError(d)
	}
	if d.Level == compatibilityWarning {
		fmt.Fprintf(out, "! Version compatibility: %s\n  Fix: %s\n", d.Detail, d.Fix)
	}
	return nil
}

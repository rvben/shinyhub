package deployfail

import "strings"

// Classify maps a deploy error to its Kind. It is computed from the error
// chain produced by internal/deploy: a build failure ("uv sync:" / "renv
// restore:") returns before the replica loop, while readiness and crash errors
// are joined under "all replicas failed health check" yet remain distinguishable
// by their underlying substrings. Order matters (see ClassifyMessage).
func Classify(err error) Kind {
	if err == nil {
		return ""
	}
	return ClassifyMessage(err.Error())
}

// ClassifyMessage classifies a raw error message. Split out so the CLI can run
// the same logic on an old server's response body (which is a string, not an
// error). First match wins; the order is load-bearing:
//   - hook_failed before everything, because a hook error quotes the app's own
//     command back in the message. A hook invoking a binary the host lacks
//     produces the same exec-not-found text as a missing server runtime, and a
//     hook command can contain any substring the later cases key on.
//   - runtime_missing before build_failed, because a missing-uv error contains
//     both the exec-not-found text and the "uv sync:" prefix.
//   - crashed before readiness_timeout, so a mixed multi-replica aggregate
//     (one crashed, one timed out) surfaces the more actionable crash.
func ClassifyMessage(msg string) Kind {
	switch {
	case mentionsHookFailure(msg):
		return HookFailed
	case MentionsMissingExecutable(msg, "Rscript"),
		MentionsMissingExecutable(msg, "uv"),
		MentionsMissingExecutable(msg, "python3"),
		MentionsMissingExecutable(msg, "python"):
		return RuntimeMissing
	case strings.Contains(msg, "uv sync:"),
		strings.Contains(msg, "renv restore:"):
		return BuildFailed
	case strings.Contains(msg, "no app.py or app.R found"),
		strings.Contains(msg, "read manifest:"),
		strings.Contains(msg, "manifest [app] command:"):
		return BundleInvalid
	case strings.Contains(msg, "crashed on startup before becoming healthy"):
		return Crashed
	case strings.Contains(msg, "did not become healthy within"):
		return ReadinessTimeout
	default:
		return ServerError
	}
}

// MentionsMissingExecutable reports whether msg describes a missing executable
// named name, matching Go's exec "executable file not found" error text.
func MentionsMissingExecutable(msg, name string) bool {
	return strings.Contains(msg, `"`+name+`"`) && strings.Contains(msg, "executable file not found")
}

// mentionsHookFailure reports whether msg came from a manifest post-deploy hook.
// RunPostDeployHooks prefixes every failure it returns with "hook[<index>] (",
// both for a non-zero exit and for a timeout, so that prefix is the marker. The
// prefix is server-generated and never contains app-controlled text, unlike the
// command echo that follows it.
func mentionsHookFailure(msg string) bool {
	i := strings.Index(msg, "hook[")
	if i < 0 {
		return false
	}
	rest := msg[i+len("hook["):]
	end := strings.Index(rest, "]")
	if end <= 0 {
		return false
	}
	for _, r := range rest[:end] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return strings.HasPrefix(rest[end+1:], " (")
}

package cli

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// openBrowserURL opens target in the operating system's default browser. It is
// a seam so command tests can prove the exact URL without launching a real app.
var openBrowserURL = func(target string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	out, err := exec.Command(command, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(out)); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

// OpenBrowserURL opens target in the operating system's default browser. The
// caller should treat failure as a recoverable convenience failure and show a
// copyable URL instead.
func OpenBrowserURL(target string) error {
	return openBrowserURL(target)
}

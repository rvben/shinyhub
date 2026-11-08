package api

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/rvben/shinyhub/internal/auth"
)

// maxPackageInitClock bounds the wall-clock cost of this package's init.
// Every process that links internal/api pays it before main runs: the server,
// every CLI invocation, shell completion, the Docker HEALTHCHECK and every test
// binary. Anything above a few milliseconds here is work that belongs behind a
// lazy initializer or in a precomputed literal.
const maxPackageInitClock = 20 * time.Millisecond

var initTraceLine = regexp.MustCompile(`^init github\.com/rvben/shinyhub/internal/api @[0-9.]+ ms, ([0-9.]+) ms clock`)

// TestPackageInitStaysCheap re-executes this test binary with no tests
// selected and GODEBUG=inittrace=1, then reads the runtime's own measurement
// of internal/api's package initialization. Measuring the init line rather
// than process wall time keeps the budget independent of binary load and
// scheduler noise.
func TestPackageInitStaysCheap(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "GODEBUG=inittrace=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("re-exec test binary: %v", err)
		}
		t.Fatalf("re-exec test binary exited %d: %s", exitErr.ExitCode(), stderr.String())
	}

	sc := bufio.NewScanner(&stderr)
	for sc.Scan() {
		m := initTraceLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		ms, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("parse init clock %q: %v", m[1], err)
		}
		clock := time.Duration(ms * float64(time.Millisecond))
		if clock > maxPackageInitClock {
			t.Fatalf("internal/api package init took %v, budget %v: %s", clock, maxPackageInitClock, sc.Text())
		}
		return
	}
	t.Fatalf("no inittrace line for internal/api in stderr:\n%s", stderr.String())
}

// TestDummyHashMatchesLiveCost keeps the unknown-user guard honest: the dummy
// hash must be a well-formed bcrypt hash at the same cost real password hashes
// use, so comparing against it takes the same time as a real comparison.
func TestDummyHashMatchesLiveCost(t *testing.T) {
	cost, err := bcrypt.Cost([]byte(dummyHash))
	if err != nil {
		t.Fatalf("dummyHash is not a bcrypt hash: %v", err)
	}
	if cost != auth.BcryptCost {
		t.Fatalf("dummyHash cost = %d, want auth.BcryptCost = %d", cost, auth.BcryptCost)
	}
	if err := auth.VerifyPassword(dummyHash, "any-submitted-password"); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("VerifyPassword(dummyHash, ...) = %v, want ErrMismatchedHashAndPassword", err)
	}
}

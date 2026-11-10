package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rvben/shinyhub/internal/runtimesocket"
)

type reloadRequest struct {
	Version int           `json:"version"`
	Wait    time.Duration `json:"wait"`
	Check   bool          `json:"check,omitempty"`
	Apps    []ReloadApp   `json:"apps,omitempty"`
	// The caller reports its last observed blockers after its readiness budget
	// expires, so the successor can include the failure in the server journal.
	ReadinessFailure string `json:"readiness_failure,omitempty"`
}

// ReloadApp records a serving obligation from the retiring process. The
// successor must satisfy it before an acknowledged reload reports success.
type ReloadApp struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	MinimumReady int    `json:"minimum_ready"`
}

type ReloadServingCheck struct {
	Snapshot func() ([]ReloadApp, error)
	Check    func([]ReloadApp) string
}

type ReloadResult struct {
	PreviousPID    int         `json:"previous_pid"`
	PID            int         `json:"pid"`
	Error          string      `json:"error,omitempty"`
	Ready          bool        `json:"ready,omitempty"`
	Apps           []ReloadApp `json:"apps,omitempty"`
	NotReadyReason string      `json:"not_ready_reason,omitempty"`
}

func reloadSocket(pidFile string, pid int) string {
	// A short name leaves room for the runtime directory in Unix's path limit.
	return filepath.Join(filepath.Dir(pidFile), ".reload", fmt.Sprintf("r%d.sock", pid))
}

func readPID(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, fmt.Errorf("read server.pid_file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, errors.New("server.pid_file does not contain a positive PID")
	}
	return pid, nil
}

// StartReloadControl exposes an acknowledged reload only to users with access
// to the server's private runtime directory. Each process has its own socket,
// so a retiring process cannot unlink its successor's control endpoint.
func StartReloadControl(ctx context.Context, pidFile string, upgradeTimeout time.Duration, upg Upgrader, state *SignalState, ready func() bool, log *slog.Logger, serving ...ReloadServingCheck) (func(), error) {
	if pidFile == "" {
		return func() {}, nil
	}
	listener, err := runtimesocket.Listen(reloadSocket(pidFile, os.Getpid()))
	if err != nil {
		return nil, err
	}
	controlCtx, cancel := context.WithCancel(ctx)
	var once sync.Once
	var handlers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := runtimesocket.Accept(listener, log)
			if err != nil {
				return
			}
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var request reloadRequest
				result := ReloadResult{PreviousPID: os.Getpid()}
				if err := json.NewDecoder(io.LimitReader(conn, 64*1024)).Decode(&request); err != nil {
					result.Error = "invalid reload request"
				} else if request.Version != 1 || request.Wait < 0 || request.Wait > 24*time.Hour {
					result.Error = "unsupported reload request or wait outside 0..24h"
				} else if request.Check && request.ReadinessFailure != "" {
					result.PID = os.Getpid()
					log.Error("reload handoff accepted but successor readiness timed out", "pid", os.Getpid(), "blockers", request.ReadinessFailure)
				} else if request.Check {
					result.PID = os.Getpid()
					result.Ready = ready()
					if result.Ready && len(request.Apps) > 0 {
						if len(serving) == 0 || serving[0].Check == nil {
							result.Error = "successor cannot verify serving app readiness"
							result.Ready = false
						} else {
							result.NotReadyReason = serving[0].Check(request.Apps)
							result.Ready = result.NotReadyReason == ""
						}
					}
				} else {
					if len(serving) > 0 && serving[0].Snapshot != nil {
						apps, snapshotErr := serving[0].Snapshot()
						if snapshotErr != nil {
							result.Error = "capture serving apps before reload: " + snapshotErr.Error()
							_ = json.NewEncoder(conn).Encode(result)
							return
						}
						result.Apps = apps
					}
					_ = conn.SetDeadline(time.Now().Add(request.Wait + upgradeTimeout + 10*time.Second))
					// Cancel a waiting request when its caller disconnects. The
					// accepted handoff itself retains the upgrader's own timeout.
					requestCtx, requestCancel := context.WithCancel(controlCtx)
					defer requestCancel()
					go func() { _, _ = io.Copy(io.Discard, conn); requestCancel() }()
					log.Info("acknowledged reload requested", "wait", request.Wait)
					if err := state.Reload(requestCtx, upg, request.Wait, log); err != nil {
						result.Error = err.Error()
						log.Error("acknowledged reload failed; continuing to serve", "err", err)
					} else if pid, err := readPID(pidFile); err != nil {
						result.Error = fmt.Sprintf("handoff succeeded but reading successor PID failed: %v", err)
					} else {
						result.PID = pid
						// Admitted deployments may have finished while --wait
						// held the reload. Verify those apps too, while retaining
						// obligations captured before any transition could stop them.
						if len(serving) > 0 && serving[0].Snapshot != nil {
							apps, err := serving[0].Snapshot()
							if err != nil {
								result.Error = "handoff accepted, but capturing serving apps after waiting failed: " + err.Error()
							} else {
								result.Apps = mergeReloadApps(result.Apps, apps)
							}
						}
						log.Info("reload handoff accepted; caller will verify successor readiness", "pid", pid)
					}
				}
				_ = json.NewEncoder(conn).Encode(result)
			}()
		}
	}()
	return func() {
		once.Do(func() {
			_ = listener.Close()
			<-acceptDone
			// A successful request has recorded its handoff before the server
			// enters retirement; let that handler deliver its acknowledgement.
			cancel()
			handlers.Wait()
		})
	}, nil
}

func mergeReloadApps(original, latest []ReloadApp) []ReloadApp {
	type identity struct {
		id   int64
		slug string
	}
	indices := make(map[identity]int, len(original)+len(latest))
	for i, app := range original {
		indices[identity{app.ID, app.Slug}] = i
	}
	for _, app := range latest {
		key := identity{app.ID, app.Slug}
		if i, ok := indices[key]; ok {
			original[i].MinimumReady = max(original[i].MinimumReady, app.MinimumReady)
		} else {
			indices[key] = len(original)
			original = append(original, app)
		}
	}
	return original
}

// RequestReload sends one correlated request to the PID currently published by
// the server. It never falls back to fire-and-forget signals on old servers.
func RequestReload(ctx context.Context, pidFile string, wait, upgradeTimeout time.Duration) (ReloadResult, error) {
	if pidFile == "" {
		return ReloadResult{}, errors.New("acknowledged reload requires server.pid_file")
	}
	if wait < 0 || wait > 24*time.Hour {
		return ReloadResult{}, errors.New("reload wait must be between 0 and 24h")
	}
	pid, err := readPID(pidFile)
	if err != nil {
		return ReloadResult{}, err
	}
	ackCtx, cancel := context.WithTimeout(ctx, wait+upgradeTimeout+10*time.Second)
	defer cancel()
	result, err := exchangeReload(ackCtx, reloadSocket(pidFile, pid), reloadRequest{Version: 1, Wait: wait})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, fmt.Errorf("reload acknowledgement interrupted; the handoff may still complete; verify the live PID before retrying: %w", err)
		}
		return result, fmt.Errorf("reload acknowledgement unavailable; server must support acknowledged reloads; verify the live PID before retrying: %w", err)
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	if result.PreviousPID != pid || result.PID <= 0 || result.PID == pid {
		return result, errors.New("reload returned an invalid successor acknowledgement")
	}
	live, err := readPID(pidFile)
	if err != nil {
		return result, err
	}
	if live != result.PID {
		return result, fmt.Errorf("successor PID %d is no longer the published live PID (%d)", result.PID, live)
	}
	// Start a fresh budget after handoff acknowledgement. The parent may still
	// need its bounded HTTP and ownership shutdown before the successor admits
	// mutations. Time spent waiting for transitions must not consume this budget.
	ctx, readinessCancel := context.WithTimeout(ctx, upgradeTimeout+time.Minute)
	defer readinessCancel()
	notReadyReason := "control-plane recovery in progress"
	for {
		probe, err := exchangeReload(ctx, reloadSocket(pidFile, result.PID), reloadRequest{Version: 1, Check: true, Apps: result.Apps})
		if err != nil {
			if ctx.Err() != nil {
				return reloadReadinessFailure(ctx, pidFile, result, notReadyReason, ctx.Err())
			}
			// A socket deadline can fire just before the context timer does.
			if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
				return reloadReadinessFailure(ctx, pidFile, result, notReadyReason, context.DeadlineExceeded)
			}
			return result, fmt.Errorf("handoff accepted, but successor readiness could not be verified (%s); check the live PID before retrying: %w", notReadyReason, err)
		}
		if probe.Error != "" {
			return result, errors.New(probe.Error)
		}
		if probe.PID != result.PID {
			return result, errors.New("successor readiness response has the wrong PID")
		}
		live, err := readPID(pidFile)
		if err != nil {
			return result, err
		}
		if live != result.PID {
			return result, fmt.Errorf("live PID changed while waiting for successor %d", result.PID)
		}
		if probe.Ready {
			result.Ready = true
			return result, nil
		}
		if probe.NotReadyReason != "" {
			notReadyReason = probe.NotReadyReason
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return reloadReadinessFailure(ctx, pidFile, result, notReadyReason, ctx.Err())
		case <-timer.C:
		}
	}
}

func reloadReadinessFailure(ctx context.Context, pidFile string, result ReloadResult, reason string, cause error) (ReloadResult, error) {
	result.NotReadyReason = reason
	if errors.Is(cause, context.DeadlineExceeded) {
		// The readiness context has expired. Use a fresh, tightly bounded budget
		// for the diagnostic, never another handoff request. Older servers ignore
		// the extra field, preserving protocol compatibility during upgrades.
		reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = exchangeReload(reportCtx, reloadSocket(pidFile, result.PID), reloadRequest{Version: 1, Check: true, ReadinessFailure: reason})
	}
	return result, fmt.Errorf("handoff accepted, but successor %d did not become ready (%s): %w", result.PID, reason, cause)
}

func exchangeReload(ctx context.Context, path string, request reloadRequest) (ReloadResult, error) {
	result, err := exchangeReloadConn(ctx, path, request)
	if err != nil {
		// Cancellation closes the socket. Preserve the cause for callers instead
		// of exposing that incidental transport error. Never discard a valid ack.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		// The socket deadline can fire just before the context timer does.
		if deadline, ok := ctx.Deadline(); ok && errors.Is(err, os.ErrDeadlineExceeded) && !time.Now().Before(deadline) {
			return result, context.DeadlineExceeded
		}
	}
	return result, err
}

func exchangeReloadConn(ctx context.Context, path string, request reloadRequest) (ReloadResult, error) {
	conn, err := runtimesocket.DialContext(ctx, path)
	if err != nil {
		return ReloadResult{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return ReloadResult{}, err
	}
	var result ReloadResult
	err = json.NewDecoder(io.LimitReader(conn, 64*1024)).Decode(&result)
	return result, err
}

// Package nativebroker implements the authenticated transport for isolated native
// workers. Only the separate Linux broker binary runs with elevated privileges.
package nativebroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

const MaxMessage = 128 * 1024
const MaxFiles = 130

type App struct {
	ID         int64  `json:"id"`
	Slug       string `json:"slug"`
	UID        int    `json:"uid"`
	GID        int    `json:"gid"`
	BundleRoot string `json:"bundle_root"`
	DataRoot   string `json:"data_root"`
	CacheRoot  string `json:"cache_root"`
}
type Policy struct {
	ControlUID int    `json:"control_uid"`
	ControlGID int    `json:"control_gid"`
	Socket     string `json:"socket"`
	StateDir   string `json:"state_dir"`
	RuntimeDir string `json:"runtime_dir"`
	Apps       []App  `json:"apps"`
}
type Launch struct {
	AppID         int64             `json:"app_id"`
	Slug          string            `json:"slug"`
	Kind          string            `json:"kind"`
	Dir           string            `json:"dir"`
	Argv          []string          `json:"argv"`
	Env           []string          `json:"env"`
	DataDir       string            `json:"data_dir,omitempty"`
	CacheDir      string            `json:"cache_dir,omitempty"`
	MemoryMB      int               `json:"memory_mb,omitempty"`
	CPUPercent    int               `json:"cpu_percent,omitempty"`
	Guarded       bool              `json:"guarded"`
	LifetimeCount int               `json:"lifetime_count"`
	Labels        map[string]string `json:"labels,omitempty"`
}
type Limits struct {
	MemoryMB   *int `json:"memory_mb,omitempty"`
	CPUPercent *int `json:"cpu_percent,omitempty"`
}
type Request struct {
	ReclaimFraction float64 `json:"reclaim_fraction,omitempty"`
	Limits          *Limits `json:"limits,omitempty"`
	Op              string  `json:"op"`
	Launch          *Launch `json:"launch,omitempty"`
	Unit            string  `json:"unit,omitempty"`
	Signal          int     `json:"signal,omitempty"`
	Cursor          string  `json:"cursor,omitempty"`
}
type State struct {
	Unit      string            `json:"unit"`
	PID       int               `json:"pid"`
	Active    bool              `json:"active"`
	Ready     bool              `json:"ready"`
	Populated bool              `json:"populated"`
	Frozen    bool              `json:"frozen"`
	Code      int               `json:"code"`
	Signaled  bool              `json:"signaled"`
	Labels    map[string]string `json:"labels,omitempty"`
}
type Response struct {
	Freed      bool    `json:"freed,omitempty"`
	Error      string  `json:"error,omitempty"`
	State      *State  `json:"state,omitempty"`
	States     []State `json:"states,omitempty"`
	Policy     *Policy `json:"policy,omitempty"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type Client struct{ Socket string }

func (c Client) Call(ctx context.Context, req Request, files []*os.File) (Response, error) {
	if len(files) > MaxFiles {
		return Response{}, errors.New("too many native launch descriptors")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	if len(raw) > MaxMessage {
		return Response{}, errors.New("native broker request is too large")
	}
	dialer := net.Dialer{Timeout: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "unixpacket", c.Socket)
	var conn *net.UnixConn
	if err == nil {
		conn = connection.(*net.UnixConn)
	}
	if err != nil {
		return Response{}, fmt.Errorf("connect native isolation broker: %w", err)
	}
	defer conn.Close()
	if err := verifyServer(conn); err != nil {
		return Response{}, err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	if err := sendPacket(conn, raw, files); err != nil {
		return Response{}, err
	}
	response, received, err := receivePacket(conn)
	for _, f := range received {
		_ = f.Close()
	}
	if err != nil {
		return Response{}, err
	}
	if len(received) > 0 {
		return Response{}, errors.New("unexpected broker response descriptors")
	}
	var result Response
	if err := json.Unmarshal(response, &result); err != nil {
		return Response{}, errors.New("invalid native broker response")
	}
	if result.Error != "" {
		return result, fmt.Errorf("native isolation broker: %s", result.Error)
	}
	return result, nil
}

package localrun

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rvben/shinyhub/internal/deploy"
)

const dataGenerationFile = ".shinyhub-local-generation"

type seedRecord struct {
	Owner      string `json:"owner"`
	Generation string `json:"generation"`
}

type seedState struct {
	Version    int                   `json:"version"`
	InProgress bool                  `json:"in_progress"`
	Records    map[string]seedRecord `json:"records"`
}

func normalizeSeedMode(mode string) (string, error) {
	if mode == "true" {
		mode = "always"
	}
	if mode == "false" {
		mode = "never"
	}
	if mode == "" {
		mode = "never"
	}
	switch mode {
	case "never", "always", "missing":
		return mode, nil
	}
	return "", fmt.Errorf("invalid seed policy %q: use never, missing, or always", mode)
}

func seedStatePath(w *workspace) string {
	return strings.TrimSuffix(w.dataLock.Name(), ".lock") + ".seed.json"
}

// The exclusive data fence serializes all readers and mutations of this state.
func loadSeedState(w *workspace) (seedState, error) {
	state := seedState{Version: 1, Records: map[string]seedRecord{}}
	data, err := os.ReadFile(seedStatePath(w))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("read local initialization state: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode local initialization state: %w", err)
	}
	if state.Version != 1 {
		return state, fmt.Errorf("unsupported local initialization state version %d", state.Version)
	}
	if state.Records == nil {
		state.Records = map[string]seedRecord{}
	}
	return state, nil
}

func saveSeedState(w *workspace, state seedState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeLocalState(seedStatePath(w), data)
}

func seedIdentity(slug string, schedule deploy.ScheduleSpec) (key, owner string) {
	owner = slug + "\x00" + schedule.Name
	// Command is already parsed by LoadManifest; equivalent quoting yields the
	// same identity. Code and credentials intentionally do not determine freshness.
	canonical, _ := json.Marshal([]any{slug, schedule.Name, schedule.Command})
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", digest), owner
}

func dataGeneration(dir string) (string, error) {
	path := filepath.Join(dir, dataGenerationFile)
	data, err := os.ReadFile(path)
	if err == nil {
		value := string(data)
		if raw, err := hex.DecodeString(value); err == nil && len(raw) == 16 {
			return value, nil
		}
		return "", fmt.Errorf("invalid local data generation marker: %s", path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read local data generation: %w", err)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	value := hex.EncodeToString(random[:])
	if err := writeLocalState(path, []byte(value)); err != nil {
		return "", err
	}
	return value, nil
}

// Sync both the replacement and its directory before acknowledging a success
// or admitting a producer; a crash must not resurrect an earlier success.
func writeLocalState(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".shinyhub-state-*")
	if err != nil {
		return fmt.Errorf("create local state: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

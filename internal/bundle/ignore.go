package bundle

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// IgnoreMatcher is the effective per-tree ignore policy shared by archive
// walking and explicit file-input validation.
type IgnoreMatcher struct {
	Filename    string
	HasNegation bool
	matcher     *gitignore.GitIgnore
}

// LoadIgnoreMatcher loads .shinyhubignore when present, otherwise .gitignore.
// The precedence is part of the bundle contract and must be shared by every
// caller deciding whether a path ships.
func LoadIgnoreMatcher(dir string) (*IgnoreMatcher, error) {
	for _, name := range []string{".shinyhubignore", ".gitignore"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err == nil {
			return &IgnoreMatcher{
				Filename:    name,
				HasNegation: ignoreFileHasNegation(raw),
				matcher:     gitignore.CompileIgnoreLines(strings.Split(string(raw), "\n")...),
			}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
	}
	return nil, nil
}

func (m *IgnoreMatcher) MatchesPath(path string) bool {
	return m != nil && m.matcher.MatchesPath(path)
}

func ignoreFileHasNegation(raw []byte) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "!") {
			return true
		}
	}
	return false
}

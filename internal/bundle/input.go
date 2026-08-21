package bundle

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileInputSpec identifies a manifest-relative source file and its desired
// forward-slash path inside an app bundle.
type FileInputSpec struct {
	From string
	To   string
}

// ResolvedFileInput is a filesystem-validated input ready to snapshot.
type ResolvedFileInput struct {
	SourcePath string
	From       string
	To         string
	info       fs.FileInfo
}

// FileInputSnapshot is the immutable content added to one or more bundles.
type FileInputSnapshot struct {
	From string
	To   string
	Mode fs.FileMode
	Data []byte
}

// ResolveFileInputs validates sources and destinations without reading file
// bodies. SnapshotFileInputs performs the bounded reads after every consumer
// bundle has passed resolution.
func ResolveFileInputs(manifestRoot, bundleRoot string, specs []FileInputSpec) ([]ResolvedFileInput, error) {
	canonicalRoot, err := filepath.EvalSymlinks(manifestRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest root: %w", err)
	}
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve manifest root: %w", err)
	}
	bundleInfo, err := os.Stat(bundleRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve bundle root: %w", err)
	}
	if !bundleInfo.IsDir() {
		return nil, fmt.Errorf("bundle root must be a directory")
	}
	bundleRoot, err = filepath.Abs(bundleRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve bundle root: %w", err)
	}
	matcher, err := LoadIgnoreMatcher(bundleRoot)
	if err != nil {
		return nil, fmt.Errorf("load bundle ignore file: %w", err)
	}
	rules := DefaultRules()

	resolved := make([]ResolvedFileInput, 0, len(specs))
	var destinations []string
	for _, spec := range specs {
		if err := validateInputRelativePath(spec.From); err != nil {
			return nil, fmt.Errorf("from %q: %w", spec.From, err)
		}
		if err := validateInputRelativePath(spec.To); err != nil {
			return nil, fmt.Errorf("to %q: %w", spec.To, err)
		}
		sourcePath := canonicalRoot
		var info fs.FileInfo
		for _, component := range strings.Split(spec.From, "/") {
			sourcePath = filepath.Join(sourcePath, component)
			info, err = os.Lstat(sourcePath)
			if err != nil {
				return nil, fmt.Errorf("from %q: %w", spec.From, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("from %q: symlink component %q is not allowed", spec.From, component)
			}
		}
		if info == nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("from %q must be a regular file", spec.From)
		}
		if decision := rules.Inspect(spec.To, info.Size()); decision != FilterAccept {
			return nil, fmt.Errorf("to %q is not allowed by bundle policy: %s", spec.To, decision)
		}
		if err := validateInputDestination(bundleRoot, spec.To, matcher); err != nil {
			return nil, fmt.Errorf("to %q: %w", spec.To, err)
		}
		for _, previous := range destinations {
			if inputDestinationConflict(previous, spec.To) {
				return nil, fmt.Errorf("destination conflict: %q conflicts with %q", spec.To, previous)
			}
		}
		destinations = append(destinations, spec.To)
		resolved = append(resolved, ResolvedFileInput{
			SourcePath: sourcePath,
			From:       spec.From,
			To:         spec.To,
			info:       info,
		})
	}
	return resolved, nil
}

func validateInputDestination(bundleRoot, to string, matcher *IgnoreMatcher) error {
	components := strings.Split(to, "/")
	for i := 1; i < len(components); i++ {
		ancestorSlash := strings.Join(components[:i], "/")
		if matcher.MatchesPath(ancestorSlash + "/") {
			return fmt.Errorf("ignored ancestor %q", ancestorSlash)
		}
		ancestor := filepath.Join(bundleRoot, filepath.FromSlash(ancestorSlash))
		info, err := os.Lstat(ancestor)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("existing ancestor %q is a symlink", ancestorSlash)
		case err == nil && !info.IsDir():
			return fmt.Errorf("existing ancestor %q is not a directory", ancestorSlash)
		case err != nil && !os.IsNotExist(err):
			return fmt.Errorf("inspect ancestor %q: %w", ancestorSlash, err)
		}
	}
	if matcher.MatchesPath(to) {
		return fmt.Errorf("destination is ignored by %s", matcher.Filename)
	}
	if _, err := os.Lstat(filepath.Join(bundleRoot, filepath.FromSlash(to))); err == nil {
		return fmt.Errorf("destination already exists in the app source")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	return nil
}

func inputDestinationConflict(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// SnapshotFileInputs reads resolved inputs once and normalizes their archive
// mode to the owner-executable bit used by the bundle digest.
func SnapshotFileInputs(inputs []ResolvedFileInput) ([]FileInputSnapshot, error) {
	out := make([]FileInputSnapshot, 0, len(inputs))
	rules := DefaultRules()
	for _, input := range inputs {
		f, err := os.Open(input.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("open from %q: %w", input.From, err)
		}
		info, statErr := f.Stat()
		if statErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("inspect from %q: %w", input.From, statErr)
		}
		if !info.Mode().IsRegular() || input.info == nil || !os.SameFile(input.info, info) {
			_ = f.Close()
			return nil, fmt.Errorf("from %q changed after resolution", input.From)
		}
		if decision := rules.Inspect(input.To, info.Size()); decision != FilterAccept {
			_ = f.Close()
			return nil, fmt.Errorf("from %q changed after resolution: %s", input.From, decision)
		}
		reader := io.Reader(f)
		if rules.MaxFileBytes > 0 {
			reader = io.LimitReader(f, rules.MaxFileBytes+1)
		}
		data, readErr := io.ReadAll(reader)
		closeErr := f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read from %q: %w", input.From, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close from %q: %w", input.From, closeErr)
		}
		if rules.MaxFileBytes > 0 && int64(len(data)) > rules.MaxFileBytes {
			return nil, fmt.Errorf("from %q changed after resolution: %s", input.From, FilterRejectFileSize)
		}
		mode := fs.FileMode(0o644)
		if info.Mode().Perm()&0o100 != 0 {
			mode = 0o755
		}
		out = append(out, FileInputSnapshot{
			From: input.From,
			To:   input.To,
			Mode: mode,
			Data: data,
		})
	}
	return out, nil
}

func validateInputRelativePath(value string) error {
	if value == "" {
		return fmt.Errorf("path is required")
	}
	if strings.Contains(value, `\`) || filepath.IsAbs(value) || strings.HasPrefix(value, "/") ||
		looksLikeWindowsInputAbsolutePath(value) || strings.HasSuffix(value, "/") {
		return fmt.Errorf("must be a normalized forward-slash relative path")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("must contain no empty, . or .. components")
		}
	}
	return nil
}

func looksLikeWindowsInputAbsolutePath(value string) bool {
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') ||
		(value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && value[2] == '/'
}

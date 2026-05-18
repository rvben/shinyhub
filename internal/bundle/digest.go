package bundle

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
)

// DigestZipReader computes a stable content digest over the accepted entries
// of a deploy bundle. Client (CLI) and server compute it from the same
// produced zip so the two cannot drift.
//
// Definition: apply Rules.Inspect to every entry.
//   - FilterAccept regular file -> contributes (name, execBit, sha256(body)).
//   - FilterSkipCacheDir        -> ignored (matches extractor behavior).
//   - any FilterReject*         -> hard error (the live bundle rejects it too).
//
// Directory entries never contribute. Entries are sorted by name; the digest
// is sha256 over a length-prefixed serialization so no field boundary is
// ambiguous. A duplicate accepted name is a hard error.
func DigestZipReader(zr *zip.Reader) (string, error) {
	rules := DefaultRules()

	type ent struct {
		name string
		exec byte
		sum  [sha256.Size]byte
	}
	var ents []ent
	seen := make(map[string]struct{})

	for _, f := range zr.File {
		decision := rules.Inspect(f.Name, int64(f.UncompressedSize64))
		switch decision {
		case FilterAccept:
			// fall through to processing below
		case FilterSkipCacheDir:
			continue
		default:
			return "", fmt.Errorf("bundle digest: rejected entry %q: %s", f.Name, decision)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if _, dup := seen[f.Name]; dup {
			return "", fmt.Errorf("bundle digest: duplicate entry %q", f.Name)
		}
		seen[f.Name] = struct{}{}

		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("bundle digest: open %q: %w", f.Name, err)
		}
		h := sha256.New()
		if _, err := io.Copy(h, rc); err != nil {
			rc.Close()
			return "", fmt.Errorf("bundle digest: read %q: %w", f.Name, err)
		}
		rc.Close()

		var exec byte
		if f.Mode().Perm()&0o100 != 0 {
			exec = 1
		}
		var e ent
		e.name = f.Name
		e.exec = exec
		copy(e.sum[:], h.Sum(nil))
		ents = append(ents, e)
	}

	sort.Slice(ents, func(i, j int) bool { return ents[i].name < ents[j].name })

	top := sha256.New()
	var lenbuf [8]byte
	for _, e := range ents {
		binary.BigEndian.PutUint64(lenbuf[:], uint64(len(e.name)))
		top.Write(lenbuf[:])
		top.Write([]byte(e.name))
		top.Write([]byte{e.exec})
		top.Write(e.sum[:])
	}
	return "sha256:" + hex.EncodeToString(top.Sum(nil)), nil
}

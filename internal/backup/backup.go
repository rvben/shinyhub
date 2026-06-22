// Package backup implements first-class snapshot and restore of all ShinyHub
// durable state: the database, deployed app bundles, and per-app data dirs. A
// backup is a single .tar.gz containing a transactionally consistent DB
// snapshot (SQLite VACUUM INTO, or pg_dump custom-format for Postgres), the
// apps and app-data trees, and a manifest recording the producing binary
// version, schema version, and DB backend. Postgres backups shell out to
// pg_dump/pg_restore, which must be on PATH.
//
// RPO/RTO: `backup` is point-in-time and safe to run on a live server, so the
// recovery point objective is "as fresh as your last scheduled backup" (run it
// from cron as often as your tolerated data loss window). `restore` is offline
// (stop the server first) and rebuilds state in place after moving the current
// state aside, so the recovery time objective is dominated by archive size,
// typically minutes. Always rehearse the restore drill (see SECURITY.md /
// docs) against a scratch directory before you need it for real.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// Manifest is the metadata header stored at manifest.json inside the archive.
type Manifest struct {
	ShinyHubVersion string `json:"shinyhub_version"`
	SchemaVersion   int    `json:"schema_version"`
	CreatedAt       string `json:"created_at"`
	// Backend names the DB engine the archive was produced from: "sqlite" (the
	// db.sqlite entry is a VACUUM INTO snapshot) or "postgres" (the db.dump
	// entry is a pg_dump custom-format archive). Empty in archives written by
	// pre-Backend binaries; those are always SQLite, so a blank value is read
	// as "sqlite" for backward compatibility.
	Backend string `json:"backend,omitempty"`
}

const (
	manifestEntry = "manifest.json"
	dbEntry       = "db.sqlite"
	dbDumpEntry   = "db.dump"
	appsPrefix    = "apps/"
	appDataPrefix = "app-data/"

	backendSQLite   = "sqlite"
	backendPostgres = "postgres"
)

// dbFilePath extracts the on-disk SQLite file path from a DSN, stripping any
// "?param=..." pragma suffix. It returns ok=false for in-memory databases,
// which cannot be backed up.
func dbFilePath(dsn string) (path string, ok bool) {
	if strings.Contains(dsn, ":memory:") || strings.Contains(dsn, "mode=memory") {
		return "", false
	}
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		dsn = dsn[:i]
	}
	dsn = strings.TrimPrefix(dsn, "file:")
	if dsn == "" {
		return "", false
	}
	return dsn, true
}

// pathWithin reports whether target resolves to base itself or a path beneath
// it. Both are made absolute and cleaned first. It does not resolve symlinks
// (target typically does not exist yet), so it is a best-effort guard.
func pathWithin(base, target string) (bool, error) {
	ab, err := filepath.Abs(base)
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", base, err)
	}
	at, err := filepath.Abs(target)
	if err != nil {
		return false, fmt.Errorf("resolve %s: %w", target, err)
	}
	ab = filepath.Clean(ab)
	at = filepath.Clean(at)
	return at == ab || strings.HasPrefix(at, ab+string(os.PathSeparator)), nil
}

// Create writes a consistent backup archive of all durable state to outPath.
func Create(cfg *config.Config, version, outPath string) error {
	postgres := db.IsPostgresDSN(cfg.Database.DSN)

	// SQLite snapshots write next to the live DB file; Postgres has no local
	// file, so dbPath stays empty and the pg_dump output lands beside the
	// archive instead.
	var dbPath string
	if !postgres {
		var ok bool
		dbPath, ok = dbFilePath(cfg.Database.DSN)
		if !ok {
			return fmt.Errorf("database %q is in-memory; nothing to back up", cfg.Database.DSN)
		}
	}

	// The output archive must not live inside a tree we are about to walk:
	// addTree would otherwise capture the partially written .partial file,
	// producing a self-containing archive that can corrupt or grow until the
	// disk fills.
	for _, root := range []string{cfg.Storage.AppsDir, cfg.Storage.AppDataDir} {
		within, err := pathWithin(root, outPath)
		if err != nil {
			return err
		}
		if within {
			return fmt.Errorf("--out %q is inside backed-up dir %q; write the archive elsewhere", outPath, root)
		}
	}

	store, err := db.Open(cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	schemaVer, err := store.SchemaVersion()
	if err != nil {
		_ = store.Close()
		return err
	}

	// Stage the DB snapshot in a temp dir on a filesystem we can write: next to
	// the live SQLite file (same device as VACUUM INTO wants), or beside the
	// archive for Postgres.
	snapBase, snapName, dbArchiveEntry, backend := filepath.Dir(outPath), dbDumpEntry, dbDumpEntry, backendPostgres
	if !postgres {
		snapBase, snapName, dbArchiveEntry, backend = filepath.Dir(dbPath), dbEntry, dbEntry, backendSQLite
	}
	snapDir, err := os.MkdirTemp(snapBase, "shinyhub-snap-")
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("create snapshot tmp dir: %w", err)
	}
	defer os.RemoveAll(snapDir)
	snapPath := filepath.Join(snapDir, snapName)
	if postgres {
		err = pgDump(cfg.Database.DSN, snapPath)
	} else {
		err = store.BackupTo(snapPath)
	}
	if err != nil {
		_ = store.Close()
		return err
	}
	_ = store.Close()

	tmpOut := outPath + ".partial"
	// Owner-only: the archive contains the full database (password and API-key
	// hashes, the audit log) plus all app source and data.
	out, err := os.OpenFile(tmpOut, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmpOut, err)
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	manifest := Manifest{
		ShinyHubVersion: version,
		SchemaVersion:   schemaVer,
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		Backend:         backend,
	}
	if err := writeManifest(tw, manifest); err != nil {
		closeAll(tw, gz, out)
		_ = os.Remove(tmpOut)
		return err
	}
	if err := addFile(tw, snapPath, dbArchiveEntry); err != nil {
		closeAll(tw, gz, out)
		_ = os.Remove(tmpOut)
		return err
	}
	if err := addTree(tw, cfg.Storage.AppsDir, appsPrefix); err != nil {
		closeAll(tw, gz, out)
		_ = os.Remove(tmpOut)
		return err
	}
	if err := addTree(tw, cfg.Storage.AppDataDir, appDataPrefix); err != nil {
		closeAll(tw, gz, out)
		_ = os.Remove(tmpOut)
		return err
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		_ = out.Close()
		_ = os.Remove(tmpOut)
		return fmt.Errorf("finalize tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		_ = out.Close()
		_ = os.Remove(tmpOut)
		return fmt.Errorf("finalize gzip: %w", err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpOut)
		return fmt.Errorf("close %s: %w", tmpOut, err)
	}
	if err := os.Rename(tmpOut, outPath); err != nil {
		_ = os.Remove(tmpOut)
		return fmt.Errorf("finalize %s: %w", outPath, err)
	}
	return nil
}

// ReadManifest returns the manifest from an archive without extracting it.
func ReadManifest(archivePath string) (Manifest, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("gzip %s: %w", archivePath, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return Manifest{}, fmt.Errorf("%s: no manifest entry", archivePath)
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Name == manifestEntry {
			var m Manifest
			if err := json.NewDecoder(tr).Decode(&m); err != nil {
				return Manifest{}, fmt.Errorf("decode manifest: %w", err)
			}
			return m, nil
		}
	}
}

// Restore rebuilds durable state from archivePath. The server must be stopped.
// It refuses archives produced by a newer schema (this binary cannot run them),
// preserves the current state (never deletes — that is your rollback path),
// then rebuilds in place. For SQLite the current DB file and app/app-data trees
// are moved aside with a ".pre-restore-<ts>" suffix and the archive is extracted
// over them. For Postgres the current database is first dumped to a
// "pre-restore-<ts>.dump" beside the archive (the rollback), then the archive's
// pg_dump is loaded with pg_restore --clean; the app/app-data trees are still
// moved aside. The archive backend must match the configured database. The
// returned slice lists every path that was preserved.
func Restore(cfg *config.Config, archivePath string) (movedAside []string, err error) {
	postgres := db.IsPostgresDSN(cfg.Database.DSN)

	var dbPath string
	if !postgres {
		var ok bool
		dbPath, ok = dbFilePath(cfg.Database.DSN)
		if !ok {
			return nil, fmt.Errorf("database %q is in-memory; cannot restore into it", cfg.Database.DSN)
		}
	}

	manifest, err := ReadManifest(archivePath)
	if err != nil {
		return nil, err
	}
	latest, err := db.LatestSchemaVersion()
	if err != nil {
		return nil, err
	}
	if manifest.SchemaVersion > latest {
		return nil, fmt.Errorf(
			"backup schema version %d is newer than this binary supports (%d); upgrade shinyhub before restoring",
			manifest.SchemaVersion, latest)
	}

	// The archive's backend must match the configured target; a pg_dump cannot
	// be loaded into SQLite and vice versa. A blank manifest backend is a
	// pre-Backend SQLite archive.
	archiveBackend := manifest.Backend
	if archiveBackend == "" {
		archiveBackend = backendSQLite
	}
	targetBackend := backendSQLite
	if postgres {
		targetBackend = backendPostgres
	}
	if archiveBackend != targetBackend {
		return nil, fmt.Errorf(
			"archive is a %s backup but the configured database is %s; restore into a matching backend",
			archiveBackend, targetBackend)
	}

	ts := time.Now().UTC().Format("20060102T150405Z")

	if postgres {
		// Rollback safety mirroring SQLite's move-aside: snapshot the current
		// target DB before pg_restore overwrites it. Recover a botched restore
		// with pg_restore of this file.
		rollback := filepath.Join(filepath.Dir(archivePath), "pre-restore-"+ts+".dump")
		if dumpErr := pgDump(cfg.Database.DSN, rollback); dumpErr != nil {
			return nil, fmt.Errorf("snapshot current db before restore: %w", dumpErr)
		}
		movedAside = append(movedAside, rollback)
	} else {
		// Move current DB file aside. Sidecars (-wal/-shm) are relocated too so a
		// stale WAL cannot graft onto the restored single-file snapshot.
		for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
			aside, mErr := preserve(p, ts)
			if mErr != nil {
				return movedAside, mErr
			}
			if aside != "" {
				movedAside = append(movedAside, aside)
			}
		}
	}

	// App source and per-app data are filesystem state regardless of DB backend.
	for _, p := range []string{cfg.Storage.AppsDir, cfg.Storage.AppDataDir} {
		aside, mErr := preserve(p, ts)
		if mErr != nil {
			return movedAside, mErr
		}
		if aside != "" {
			movedAside = append(movedAside, aside)
		}
	}

	// For SQLite the DB entry is written straight to its final path; for Postgres
	// it is staged to a temp file that pg_restore then loads.
	dbDest := dbPath
	if postgres {
		tmpDir, tErr := os.MkdirTemp(filepath.Dir(archivePath), "shinyhub-restore-")
		if tErr != nil {
			return movedAside, fmt.Errorf("create restore tmp dir: %w", tErr)
		}
		defer os.RemoveAll(tmpDir)
		dbDest = filepath.Join(tmpDir, dbDumpEntry)
	}

	if err := extract(archivePath, dbDest, cfg.Storage.AppsDir, cfg.Storage.AppDataDir); err != nil {
		return movedAside, fmt.Errorf("extract archive (previous state preserved at *.pre-restore-%s): %w", ts, err)
	}

	if postgres {
		if err := pgRestore(cfg.Database.DSN, dbDest); err != nil {
			return movedAside, fmt.Errorf("load archive into postgres (previous db preserved at pre-restore-%s.dump): %w", ts, err)
		}
	}
	return movedAside, nil
}

// preserve renames p to "p.pre-restore-<ts>" and returns the new path, or ""
// when p does not exist (nothing to keep). It never deletes.
func preserve(p, ts string) (string, error) {
	if _, statErr := os.Lstat(p); statErr != nil {
		return "", nil
	}
	aside := p + ".pre-restore-" + ts
	if mvErr := os.Rename(p, aside); mvErr != nil {
		return "", fmt.Errorf("preserve %s: %w", p, mvErr)
	}
	return aside, nil
}

// extract unpacks the archive: the DB entry (db.sqlite or db.dump, whichever
// the archive carries) is written to dbDest, and the app/app-data trees to
// their dirs. The caller decides dbDest: the live SQLite path, or a temp file
// that pg_restore consumes for Postgres.
func extract(archivePath, dbDest, appsDir, appDataDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", archivePath, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip %s: %w", archivePath, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	for _, d := range []string{filepath.Dir(dbDest), appsDir, appDataDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		switch {
		case hdr.Name == manifestEntry:
			continue
		case hdr.Name == dbEntry || hdr.Name == dbDumpEntry:
			if err := writeFile(tr, dbDest, 0o640); err != nil {
				return err
			}
		case strings.HasPrefix(hdr.Name, appsPrefix):
			if err := extractInto(tr, hdr, appsDir, appsPrefix); err != nil {
				return err
			}
		case strings.HasPrefix(hdr.Name, appDataPrefix):
			if err := extractInto(tr, hdr, appDataDir, appDataPrefix); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unexpected archive entry %q", hdr.Name)
		}
	}
}

// extractInto writes one tar entry beneath base, rejecting any path that would
// escape base (tarslip / path traversal guard).
func extractInto(tr *tar.Reader, hdr *tar.Header, base, prefix string) error {
	rel := strings.TrimPrefix(hdr.Name, prefix)
	dest := filepath.Join(base, filepath.Clean("/"+rel))
	cleanBase := filepath.Clean(base)
	if dest != cleanBase && !strings.HasPrefix(dest, cleanBase+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry %q escapes %s", hdr.Name, base)
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(dest, 0o750)
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		return writeFile(tr, dest, os.FileMode(hdr.Mode)&0o777)
	default:
		return fmt.Errorf("unsupported archive entry type %d for %q", hdr.Typeflag, hdr.Name)
	}
}

func writeFile(r io.Reader, dest string, mode os.FileMode) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
	}
	return nil
}

func writeManifest(tw *tar.Writer, m Manifest) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	hdr := &tar.Header{
		Name:    manifestEntry,
		Mode:    0o600,
		Size:    int64(len(body)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, src, name string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer f.Close()
	hdr := &tar.Header{
		Name:     name,
		Mode:     int64(fi.Mode().Perm()),
		Size:     fi.Size(),
		ModTime:  fi.ModTime(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write header %s: %w", name, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// addTree walks root and writes every dir/regular file under it into the
// archive under prefix. A missing root is not an error (a fresh install may
// have no apps yet).
func addTree(tw *tar.Writer, root, prefix string) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := prefix + filepath.ToSlash(rel)
		switch {
		case fi.IsDir():
			return tw.WriteHeader(&tar.Header{
				Name:     name + "/",
				Mode:     0o750,
				Typeflag: tar.TypeDir,
				ModTime:  fi.ModTime(),
			})
		case fi.Mode().IsRegular():
			return addFile(tw, p, name)
		default:
			return nil // skip sockets/symlinks/devices
		}
	})
}

func closeAll(tw *tar.Writer, gz *gzip.Writer, out *os.File) {
	_ = tw.Close()
	_ = gz.Close()
	_ = out.Close()
}

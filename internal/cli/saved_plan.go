package cli

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/bundle"
	slugpkg "github.com/rvben/shinyhub/internal/slug"
)

const (
	savedPlanFormatVersion = 1
	savedPlanKindSingleApp = "single-app"
	savedPlanMetadataPath  = "metadata.json"
	savedPlanBundlePath    = "bundle.zip"
	defaultPlanLifetime    = 24 * time.Hour
	maxSavedPlanBytes      = 512 << 20
)

type savedPlanTarget struct {
	Host             string `json:"host"`
	Resource         string `json:"resource"`
	Slug             string `json:"slug"`
	ExpectedExists   bool   `json:"expected_exists"`
	ExpectedRevision string `json:"expected_revision,omitempty"`
}

type savedPlanBundle struct {
	Digest    string `json:"digest"`
	Path      string `json:"embedded_path"`
	SizeBytes int    `json:"size_bytes"`
	FileCount int    `json:"file_count"`
}

type savedPlanDesired struct {
	Name       string `json:"name"`
	Visibility string `json:"visibility,omitempty"`
	Start      bool   `json:"start"`
}

type savedPlanCompatibility struct {
	RequiresPlanApply bool `json:"requires_plan_apply"`
	ProtocolVersion   int  `json:"protocol_version,omitempty"`
}

type savedPlanEnvelope struct {
	FormatVersion int                    `json:"format_version"`
	Kind          string                 `json:"kind"`
	PlanID        string                 `json:"plan_id"`
	CreatedAt     time.Time              `json:"created_at"`
	ExpiresAt     time.Time              `json:"expires_at"`
	Target        savedPlanTarget        `json:"target"`
	Bundle        savedPlanBundle        `json:"bundle"`
	Desired       savedPlanDesired       `json:"desired"`
	Compatibility savedPlanCompatibility `json:"compatibility"`
	Plan          deploymentPlan         `json:"plan"`
	Integrity     string                 `json:"integrity"`
}

type loadedSavedPlan struct {
	Envelope savedPlanEnvelope
	Bundle   []byte
	Path     string
}

func newSavedPlanID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create plan id: %w", err)
	}
	return "plan_" + hex.EncodeToString(raw[:]), nil
}

func savedPlanIntegrity(envelope savedPlanEnvelope, bundleBytes []byte) (string, error) {
	envelope.Integrity = ""
	metadata, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, _ = h.Write(metadata)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(bundleBytes)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func buildSavedPlan(plan deploymentPlan, bundleBytes []byte, protocolVersion int, lifetime time.Duration, now time.Time) (savedPlanEnvelope, error) {
	if lifetime <= 0 {
		return savedPlanEnvelope{}, fmt.Errorf("plan lifetime must be greater than zero")
	}
	if plan.Bundle == nil || plan.Bundle.Digest == "" || len(bundleBytes) == 0 {
		return savedPlanEnvelope{}, fmt.Errorf("plan has no exact deployment bundle")
	}
	planID, err := newSavedPlanID()
	if err != nil {
		return savedPlanEnvelope{}, err
	}
	created := now.UTC().Truncate(time.Second)
	envelope := savedPlanEnvelope{
		FormatVersion: savedPlanFormatVersion,
		Kind:          savedPlanKindSingleApp,
		PlanID:        planID,
		CreatedAt:     created,
		ExpiresAt:     created.Add(lifetime),
		Target: savedPlanTarget{
			Host: normalizeHost(plan.Host), Resource: "app/" + plan.Slug, Slug: plan.Slug,
			ExpectedExists: plan.Remote.Exists, ExpectedRevision: plan.Remote.ResourceRevision,
		},
		Bundle: savedPlanBundle{
			Digest: plan.Bundle.Digest, Path: savedPlanBundlePath,
			SizeBytes: len(bundleBytes), FileCount: plan.Bundle.FileCount,
		},
		Desired:       savedPlanDesired{Name: plan.Slug, Visibility: plan.Visibility, Start: plan.Start},
		Compatibility: savedPlanCompatibility{RequiresPlanApply: true, ProtocolVersion: protocolVersion},
		Plan:          plan,
	}
	bundleCopy := *plan.Bundle
	bundleCopy.Buffer = nil
	envelope.Plan.Bundle = &bundleCopy
	envelope.Integrity, err = savedPlanIntegrity(envelope, bundleBytes)
	return envelope, err
}

func writeSavedPlan(path string, envelope savedPlanEnvelope, bundleBytes []byte, force bool) error {
	if path == "" {
		return fmt.Errorf("saved plan path is empty")
	}
	if !force {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("saved plan %s already exists; pass --force to replace it", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	metadata, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("encode saved plan: %w", err)
	}

	var container bytes.Buffer
	zw := zip.NewWriter(&container)
	stableTime := time.Unix(0, 0).UTC()
	metaHeader := &zip.FileHeader{Name: savedPlanMetadataPath, Method: zip.Deflate}
	metaHeader.SetModTime(stableTime)
	metaWriter, err := zw.CreateHeader(metaHeader)
	if err != nil {
		return err
	}
	if _, err := metaWriter.Write(metadata); err != nil {
		return err
	}
	bundleHeader := &zip.FileHeader{Name: savedPlanBundlePath, Method: zip.Store}
	bundleHeader.SetModTime(stableTime)
	bundleWriter, err := zw.CreateHeader(bundleHeader)
	if err != nil {
		return err
	}
	if _, err := bundleWriter.Write(bundleBytes); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".shinyhub-plan-*")
	if err != nil {
		return fmt.Errorf("create saved plan: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(container.Bytes()); err != nil {
		return fmt.Errorf("write saved plan: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync saved plan: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("commit saved plan: %w", err)
	}
	removeTemp = false
	return nil
}

func readSavedPlan(path string, now time.Time, allowExpired bool) (*loadedSavedPlan, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("open saved plan %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("saved plan %s is a directory", path)
	}
	if info.Size() > maxSavedPlanBytes {
		return nil, fmt.Errorf("saved plan exceeds %s limit", humanBytes(maxSavedPlanBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read saved plan %s: %w", path, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("saved plan is corrupt: %w", err)
	}
	entries := make(map[string]*zip.File, len(zr.File))
	for _, file := range zr.File {
		if file.Name != savedPlanMetadataPath && file.Name != savedPlanBundlePath {
			return nil, fmt.Errorf("saved plan contains unexpected entry %q", file.Name)
		}
		if _, duplicate := entries[file.Name]; duplicate {
			return nil, fmt.Errorf("saved plan contains duplicate entry %q", file.Name)
		}
		entries[file.Name] = file
	}
	if len(entries) != 2 || entries[savedPlanMetadataPath] == nil || entries[savedPlanBundlePath] == nil {
		return nil, fmt.Errorf("saved plan is incomplete: expected %s and %s", savedPlanMetadataPath, savedPlanBundlePath)
	}
	readEntry := func(file *zip.File, limit int64) ([]byte, error) {
		if file.UncompressedSize64 > uint64(limit) {
			return nil, fmt.Errorf("entry %s exceeds size limit", file.Name)
		}
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		data, err := io.ReadAll(io.LimitReader(r, limit+1))
		if err == nil && int64(len(data)) > limit {
			return nil, fmt.Errorf("entry %s exceeds size limit", file.Name)
		}
		return data, err
	}
	metadata, err := readEntry(entries[savedPlanMetadataPath], 8<<20)
	if err != nil {
		return nil, fmt.Errorf("read saved plan metadata: %w", err)
	}
	bundleBytes, err := readEntry(entries[savedPlanBundlePath], maxSavedPlanBytes)
	if err != nil {
		return nil, fmt.Errorf("read saved plan bundle: %w", err)
	}
	var envelope savedPlanEnvelope
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("saved plan metadata is invalid: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("saved plan metadata contains trailing content")
	}
	if envelope.FormatVersion != savedPlanFormatVersion {
		return nil, fmt.Errorf("saved plan format %d is not supported by this CLI (supports %d)", envelope.FormatVersion, savedPlanFormatVersion)
	}
	if envelope.Kind != savedPlanKindSingleApp {
		return nil, fmt.Errorf("saved plan kind %q is not supported by `shinyhub apply`", envelope.Kind)
	}
	if envelope.PlanID == "" || !slugpkg.Valid(envelope.Target.Slug) || envelope.Target.Resource != "app/"+envelope.Target.Slug {
		return nil, fmt.Errorf("saved plan metadata is incomplete")
	}
	if envelope.Target.Host == "" || normalizeHost(envelope.Plan.Host) != normalizeHost(envelope.Target.Host) ||
		envelope.Plan.Slug != envelope.Target.Slug || envelope.Desired.Name == "" ||
		envelope.Plan.Start != envelope.Desired.Start || envelope.Plan.Visibility != envelope.Desired.Visibility {
		return nil, fmt.Errorf("saved plan target and desired state are inconsistent")
	}
	if envelope.Target.ExpectedExists && envelope.Target.ExpectedRevision == "" {
		return nil, fmt.Errorf("saved plan has no resource revision for existing app %s", envelope.Target.Slug)
	}
	if envelope.Plan.Bundle == nil || envelope.Bundle.Path != savedPlanBundlePath || envelope.Bundle.SizeBytes != len(bundleBytes) {
		return nil, fmt.Errorf("saved plan bundle metadata does not match the embedded bundle")
	}
	zipBundle, err := zip.NewReader(bytes.NewReader(bundleBytes), int64(len(bundleBytes)))
	if err != nil {
		return nil, fmt.Errorf("saved plan bundle is corrupt: %w", err)
	}
	digest, err := bundle.DigestZipReader(zipBundle)
	if err != nil {
		return nil, fmt.Errorf("saved plan bundle is invalid: %w", err)
	}
	if digest != envelope.Bundle.Digest || digest != envelope.Plan.Bundle.Digest {
		return nil, fmt.Errorf("saved plan bundle digest mismatch: artifact may be corrupt or modified")
	}
	wantIntegrity, err := savedPlanIntegrity(envelope, bundleBytes)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(wantIntegrity, envelope.Integrity) {
		return nil, fmt.Errorf("saved plan integrity check failed: artifact may be corrupt or modified")
	}
	if envelope.ExpiresAt.IsZero() || !envelope.ExpiresAt.After(envelope.CreatedAt) {
		return nil, fmt.Errorf("saved plan has an invalid expiry")
	}
	if !allowExpired && !now.Before(envelope.ExpiresAt) {
		return nil, fmt.Errorf("saved plan expired at %s; re-run `shinyhub plan`", envelope.ExpiresAt.Format(time.RFC3339))
	}
	return &loadedSavedPlan{Envelope: envelope, Bundle: bundleBytes, Path: path}, nil
}

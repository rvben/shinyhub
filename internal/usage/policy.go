package usage

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/secrets"
)

const usagePseudonymEncryptionInfo = "shinyhub-usage-pseudonym-master-v1"

type policyStore interface {
	EnsureUsagePolicy(string, []byte) (db.UsagePolicyState, error)
	UsagePolicy() (db.UsagePolicyState, error)
	SetUsagePseudonymKeyEncrypted([]byte) error
	SetUsagePolicyMode(string) (int64, error)
	AdvanceUsagePolicyGeneration() (int64, error)
	UsagePolicyForApp(string) (db.UsageAppPolicySnapshot, error)
	ListUsageAppPolicies() ([]db.UsageAppPolicy, error)
	ListUsageIdentityRows(*int64, int) ([]db.UsageIdentityRow, error)
	PseudonymizeUsageSession(string, string, int64) error
	UnattributeUsageSessions(*int64, int64) (int64, error)
}

// Policy contains the plaintext pseudonym master only in process memory.
type Policy struct {
	Mode       config.UsageIdentityMode
	mu         sync.RWMutex
	generation int64
	master     []byte
	overrides  map[string]string
	store      policyStore
}

type PolicySnapshot struct {
	IdentityMode config.UsageIdentityMode
	Generation   int64
	Collect      bool
}

// LoadOrInitPolicy loads the stable installation pseudonym master and applies
// a configured hub-mode transition before collection starts. A stricter mode
// is therefore enforced on disk as well as in API presentation.
func LoadOrInitPolicy(store policyStore, desired config.UsageIdentityMode, authSecret string) (*Policy, error) {
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		return nil, fmt.Errorf("generate usage pseudonym master: %w", err)
	}
	encKey := secrets.DeriveKeyWithInfo(authSecret, usagePseudonymEncryptionInfo)
	encrypted, err := secrets.Encrypt(encKey, master)
	if err != nil {
		return nil, fmt.Errorf("encrypt usage pseudonym master: %w", err)
	}
	state, err := store.EnsureUsagePolicy(string(desired), encrypted)
	if err != nil {
		return nil, err
	}
	if len(state.PseudonymKeyEnc) == 0 {
		if err := store.SetUsagePseudonymKeyEncrypted(encrypted); err != nil {
			return nil, fmt.Errorf("persist usage pseudonym master: %w", err)
		}
	} else {
		master, err = secrets.Decrypt(encKey, state.PseudonymKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypt usage pseudonym master (run rotate-secret before changing auth.secret): %w", err)
		}
	}

	policy := &Policy{Mode: desired, generation: state.Generation, master: master, overrides: map[string]string{}, store: store}
	if state.IdentityMode == string(desired) {
		// Reconciliation is idempotent and also completes a prior interrupted
		// downgrade. Never trust the mode marker alone as proof that every row is
		// already compliant.
		if desired != config.UsageIdentityIdentified {
			if _, err := policy.Reconcile(store, nil, desired); err != nil {
				return nil, err
			}
		}
	} else {
		generation, err := store.SetUsagePolicyMode(string(desired))
		if err != nil {
			return nil, err
		}
		policy.setGeneration(generation)
		if config.UsageIdentityRank(desired) < config.UsageIdentityRank(config.UsageIdentityMode(state.IdentityMode)) {
			if _, err := policy.Reconcile(store, nil, desired); err != nil {
				return nil, err
			}
		}
	}
	if err := policy.loadAndReconcileAppPolicies(store); err != nil {
		return nil, err
	}
	return policy, nil
}

func (p *Policy) loadAndReconcileAppPolicies(store policyStore) error {
	apps, err := store.ListUsageAppPolicies()
	if err != nil {
		return err
	}
	p.mu.Lock()
	for _, app := range apps {
		p.overrides[app.Slug] = app.Override
	}
	p.mu.Unlock()
	for _, app := range apps {
		mode, collect := config.EffectiveUsageIdentityMode(p.Mode, app.Override)
		if !collect {
			mode = config.UsageIdentityUnattributed
		}
		if mode == config.UsageIdentityIdentified {
			continue
		}
		if _, err := p.Reconcile(store, &app.AppID, mode); err != nil {
			return fmt.Errorf("reconcile usage policy for app %s: %w", app.Slug, err)
		}
	}
	return nil
}

func (p *Policy) Generation() int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.generation
}

func (p *Policy) setGeneration(generation int64) {
	p.mu.Lock()
	p.generation = generation
	p.mu.Unlock()
}

// CachedSnapshot resolves the last refreshed policy entirely in memory. The
// durable insert still clamps every queued event against the then-current
// database policy, so a stale cache can only temporarily under-collect; it
// cannot persist identity above the committed privacy ceiling.
func (p *Policy) CachedSnapshot(appSlug string) PolicySnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	mode, collect := config.EffectiveUsageIdentityMode(p.Mode, p.overrides[appSlug])
	return PolicySnapshot{IdentityMode: mode, Generation: p.generation, Collect: collect}
}

// Refresh replaces the whole in-memory policy view from durable state. A full
// replacement (rather than a merge) also removes overrides for deleted apps
// and prevents a recreated slug from inheriting its predecessor's policy.
func (p *Policy) Refresh() error {
	state, err := p.store.UsagePolicy()
	if err != nil {
		return err
	}
	apps, err := p.store.ListUsageAppPolicies()
	if err != nil {
		return err
	}
	overrides := make(map[string]string, len(apps))
	for _, app := range apps {
		overrides[app.Slug] = app.Override
	}
	p.mu.Lock()
	p.Mode = config.UsageIdentityMode(state.IdentityMode)
	p.generation = state.Generation
	p.overrides = overrides
	p.mu.Unlock()
	return nil
}

// Snapshot captures the authoritative effective policy at the successful
// WebSocket upgrade. It deliberately resolves from durable state so another
// control-plane instance's change, or deletion and recreation of the same
// slug, cannot leave this process collecting under a stale policy. The later
// insert clamps this snapshot again against the then-current committed policy.
func (p *Policy) Snapshot(appSlug string) (PolicySnapshot, error) {
	state, err := p.store.UsagePolicyForApp(appSlug)
	if err != nil {
		return PolicySnapshot{}, err
	}
	mode, collect := config.EffectiveUsageIdentityMode(config.UsageIdentityMode(state.HubMode), state.Override)
	p.mu.Lock()
	p.Mode = config.UsageIdentityMode(state.HubMode)
	p.generation = state.Generation
	p.overrides[appSlug] = state.Override
	p.mu.Unlock()
	return PolicySnapshot{IdentityMode: mode, Generation: state.Generation, Collect: collect}, nil
}

func (p *Policy) Pseudonym(appSlug string, userID int64) string {
	mac := hmac.New(sha256.New, p.master)
	mac.Write([]byte("app:"))
	mac.Write([]byte(appSlug))
	mac.Write([]byte{0})
	mac.Write([]byte(strconv.FormatInt(userID, 10)))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// Reconcile irreversibly lowers retained identity for the hub or one app.
// Raising a mode is intentionally prospective and never backfills identity.
func (p *Policy) Reconcile(store policyStore, appID *int64, target config.UsageIdentityMode) (int64, error) {
	generation := p.Generation()
	if target == config.UsageIdentityUnattributed {
		return store.UnattributeUsageSessions(appID, generation)
	}
	if target != config.UsageIdentityPseudonymous {
		return 0, nil
	}
	var changed int64
	for {
		rows, err := store.ListUsageIdentityRows(appID, 500)
		if err != nil {
			return changed, err
		}
		if len(rows) == 0 {
			return changed, nil
		}
		for _, row := range rows {
			if err := store.PseudonymizeUsageSession(row.SessionID, p.Pseudonym(row.AppSlug, row.UserID), generation); err != nil {
				return changed, err
			}
			changed++
		}
	}
}

// ReconcileApp advances the durable policy generation and irreversibly lowers
// retained identity for one app. It is used before persisting a stricter app
// override so a successful settings change never leaves older rows behind at a
// more identifying level.
func (p *Policy) ReconcileApp(store policyStore, appID int64, target config.UsageIdentityMode) (int64, error) {
	generation, err := store.AdvanceUsagePolicyGeneration()
	if err != nil {
		return 0, err
	}
	p.setGeneration(generation)
	return p.Reconcile(store, &appID, target)
}

// ApplyCommittedAppPolicy refreshes the in-memory snapshot after the app
// override and usage-policy generation have committed in one database
// transaction, then irreversibly repairs any retained rows that exceed it.
func (p *Policy) ApplyCommittedAppPolicy(store policyStore, appID int64, appSlug, override string) (int64, error) {
	state, err := store.UsagePolicy()
	if err != nil {
		return 0, err
	}
	p.mu.Lock()
	p.Mode = config.UsageIdentityMode(state.IdentityMode)
	p.generation = state.Generation
	p.overrides[appSlug] = override
	hubMode := p.Mode
	p.mu.Unlock()
	mode, collect := config.EffectiveUsageIdentityMode(hubMode, override)
	if !collect {
		mode = config.UsageIdentityUnattributed
	}
	if mode == config.UsageIdentityIdentified {
		return 0, nil
	}
	return p.Reconcile(store, &appID, mode)
}

func UsagePseudonymEncryptionKey(authSecret string) []byte {
	return secrets.DeriveKeyWithInfo(authSecret, usagePseudonymEncryptionInfo)
}

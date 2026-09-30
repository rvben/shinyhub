package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrOwnerFenced reports that the caller no longer holds the control-plane
// lease at the epoch it presented, or that the lease has expired.
var ErrOwnerFenced = errors.New("control-plane lease is stale or expired")

// OwnerLease identifies one span of control-plane ownership: the instance that
// holds the lease and the fencing epoch it acquired it at. Every acquisition
// increments the epoch, so a restarted or successor owner always presents a
// higher epoch than anything its predecessor wrote.
type OwnerLease struct {
	Instance string
	Epoch    int64
}

// checkOwnerLease confirms, inside tx, that lease still holds the cp_owner row
// at its epoch and that the lease is unexpired. The no-op UPDATE also locks the
// row, so a takeover cannot interleave with the rest of the transaction.
// PostgreSQL's transaction timestamp can precede a lock wait, so the check
// reads its live clock there.
func (s *Store) checkOwnerLease(ctx context.Context, tx writeTx, lease OwnerLease) error {
	if lease.Instance == "" || lease.Epoch <= 0 {
		return ErrOwnerFenced
	}
	now := s.d.now()
	if s.IsPostgres() {
		now = "clock_timestamp()"
	}
	res, err := tx.ExecContext(ctx, `UPDATE cp_owner SET epoch = epoch WHERE role = ? AND instance_id = ? AND epoch = ? AND expires_at > `+now, ownerRole, lease.Instance, lease.Epoch)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrOwnerFenced
	}
	return nil
}

// leasedWrite runs fn in a write transaction fenced by lease: the lease is
// checked before fn and again immediately before commit, so a takeover while
// fn waited on row locks still discards the write. A nil lease runs unfenced;
// only servers that never wire an elector (tests, embedded use) pass nil.
// fn reports whether it changed anything; nothing is committed when it did not.
func (s *Store) leasedWrite(ctx context.Context, lease *OwnerLease, fn func(tx writeTx) (bool, error)) (bool, error) {
	tx, err := s.d.beginWrite(ctx, s.rawDB(), 0)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	if lease != nil {
		if err := s.checkOwnerLease(ctx, tx, *lease); err != nil {
			return false, err
		}
	}
	changed, err := fn(tx)
	if err != nil || !changed {
		return false, err
	}
	if lease != nil {
		if err := s.checkOwnerLease(ctx, tx, *lease); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func leaseEpoch(lease *OwnerLease) int64 {
	if lease == nil {
		return 0
	}
	return lease.Epoch
}

// ClaimRedeploy records that the caller, at its lease epoch, is serving the
// settings redeploy seq for slug. It returns false without error when the seq
// must not be served: a newer seq has been launched (a later redeploy reads
// the same current settings), or seq has already been served. A stale lease
// returns ErrOwnerFenced. A claim left by an older epoch whose claimant never
// reported is overwritten, which is how a successor owner recovers it.
func (s *Store) ClaimRedeploy(ctx context.Context, lease *OwnerLease, slug string, seq int64) (bool, error) {
	if seq <= 0 {
		return false, fmt.Errorf("claim redeploy %s: invalid seq %d", slug, seq)
	}
	return s.leasedWrite(ctx, lease, func(tx writeTx) (bool, error) {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET redeploy_claim_seq = ?, redeploy_claim_epoch = ?
			 WHERE slug = ? AND redeploy_seq_launched = ? AND last_redeploy_seq < ?`,
			seq, leaseEpoch(lease), slug, seq, seq)
		if err != nil {
			return false, fmt.Errorf("claim redeploy %s: %w", slug, err)
		}
		n, err := res.RowsAffected()
		return n == 1, err
	})
}

// RedeployNeedsFullCycle reports whether a seq launched for slug since the
// last reported outcome changed more than the replica count, so serving the
// newest seq must cycle the whole pool rather than resize it. A structural seq
// superseded by a newer replica-only seq is still owed its full cycle here.
func (s *Store) RedeployNeedsFullCycle(ctx context.Context, slug string) (bool, error) {
	var full, last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT redeploy_full_seq, last_redeploy_seq FROM apps WHERE slug = ?`, slug,
	).Scan(&full, &last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("read redeploy full seq %s: %w", slug, err)
	}
	return full > last, nil
}

// owedFullSeq returns the redeploy_full_seq assignment, and its arguments,
// for recording outcome at seq. Completed and partial outcomes booted the pool
// on the stored settings and so serve any full cycle owed up to seq; only a
// value carried past the launched seq needs lowering. Failed and skipped
// outcomes did not apply the settings, so a full cycle owed at or before seq
// moves to seq+1: the next seq served, even a replica-only one, cycles the pool
// rather than reporting success on a pool still on the old settings. A newer
// structural seq owed above seq is kept either way. The right-hand side reads
// pre-update values on both SQLite and PostgreSQL.
func owedFullSeq(outcome string, seq int64) (string, []any) {
	if outcome == RedeployCompleted || outcome == RedeployPartial {
		return `redeploy_full_seq = CASE WHEN redeploy_full_seq > redeploy_seq_launched THEN redeploy_seq_launched ELSE redeploy_full_seq END`, nil
	}
	return `redeploy_full_seq = CASE WHEN redeploy_full_seq > last_redeploy_seq AND redeploy_full_seq <= ? THEN ? ELSE redeploy_full_seq END`, []any{seq, seq + 1}
}

// RecordRedeployOutcome reports how the claimed redeploy seq ended. Only the
// claimant still holding the lease at the claim's epoch can write it, and
// each seq gets at most one outcome. It returns false when the write was
// discarded because the claim is no longer this caller's or the seq was
// already reported; a stale lease returns ErrOwnerFenced.
func (s *Store) RecordRedeployOutcome(ctx context.Context, lease *OwnerLease, slug string, seq int64, outcome, reason string) (bool, error) {
	full, fullArgs := owedFullSeq(outcome, seq)
	args := append([]any{seq, outcome, reason, time.Now().Unix()}, fullArgs...)
	args = append(args, slug, seq, leaseEpoch(lease), seq)
	return s.leasedWrite(ctx, lease, func(tx writeTx) (bool, error) {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET last_redeploy_seq = ?, last_redeploy_outcome = ?, last_redeploy_reason = ?, last_redeploy_at = ?, `+full+`
			 WHERE slug = ? AND redeploy_claim_seq = ? AND redeploy_claim_epoch = ? AND last_redeploy_seq < ?`,
			args...)
		if err != nil {
			return false, fmt.Errorf("record redeploy outcome %s: %w", slug, err)
		}
		n, err := res.RowsAffected()
		return n == 1, err
	})
}

// RecordBootOutcome reports that a restart, deploy or rollback booted the pool
// on the settings carried by seq, the latest launched seq read after it took
// the app's deploy lock. Unlike RecordRedeployOutcome it may replace an
// outcome already recorded for seq (a fresh boot is the remedy for a failed
// redeploy) and it serves a still-owed seq, so a pending redeploy for it no
// longer cycles. A seq launched after that read makes it match nothing: that
// newer seq stays owed and is judged on its own outcome.
func (s *Store) RecordBootOutcome(ctx context.Context, lease *OwnerLease, appID, seq int64, outcome, reason string) (bool, error) {
	if seq <= 0 {
		return false, nil
	}
	full, fullArgs := owedFullSeq(outcome, seq)
	args := append([]any{seq, outcome, reason, time.Now().Unix()}, fullArgs...)
	args = append(args, appID, seq, seq)
	return s.leasedWrite(ctx, lease, func(tx writeTx) (bool, error) {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET last_redeploy_seq = ?, last_redeploy_outcome = ?, last_redeploy_reason = ?, last_redeploy_at = ?, `+full+`
			 WHERE id = ? AND redeploy_seq_launched = ? AND last_redeploy_seq <= ?`,
			args...)
		if err != nil {
			return false, fmt.Errorf("record restart outcome: %w", err)
		}
		n, err := res.RowsAffected()
		return n == 1, err
	})
}

// OwedRedeploy is an app whose latest launched settings redeploy has not
// reported an outcome.
type OwedRedeploy struct {
	Slug string
	Seq  int64
}

// ListOwedRedeploys returns every app with a launched settings redeploy that
// has no outcome yet, for the owner's startup recovery to relaunch.
func (s *Store) ListOwedRedeploys() ([]OwedRedeploy, error) {
	rows, err := s.db.Query(`SELECT slug, redeploy_seq_launched FROM apps WHERE redeploy_seq_launched > last_redeploy_seq ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list owed redeploys: %w", err)
	}
	defer rows.Close()
	var out []OwedRedeploy
	for rows.Next() {
		var o OwedRedeploy
		if err := rows.Scan(&o.Slug, &o.Seq); err != nil {
			return nil, fmt.Errorf("list owed redeploys: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

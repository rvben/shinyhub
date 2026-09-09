package worker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// All elastic operations serialize with fencing. The journal records intent
// before runtime creation and retains tombstones across worker restarts.
// These endpoints remain disabled unless a live reservation authorizer is wired.
type elasticJournal struct {
	Result      api.ElasticResult `json:"result"`
	Token       string            `json:"token"`
	HostPort    int               `json:"host_port"`
	MaxSessions int               `json:"max_sessions"`
}

func (s *replicaServer) journalPath(id string) string {
	return filepath.Join(s.dataDir, "elastic-launches", id+".json")
}

func (s *replicaServer) readElasticJournal(id string) (*elasticJournal, error) {
	b, err := os.ReadFile(s.journalPath(id))
	if err != nil {
		return nil, err
	}
	var j elasticJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	if j.Result.ReservationID != id {
		return nil, errors.New("journal identity mismatch")
	}
	return &j, nil
}

func (s *replicaServer) saveElasticJournal(j *elasticJournal) error {
	path := s.journalPath(j.Result.ReservationID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Persist the new journal directory's entry in the existing worker data root.
	parent, err := os.Open(s.dataDir)
	if err != nil {
		return err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".journal-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(b)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *replicaServer) elasticAuthorized(ctx context.Context, a api.ElasticAuthority, action string) error {
	id, err := uuid.Parse(a.ReservationID)
	digest, de := hex.DecodeString(a.RequestDigest)
	if err != nil || id.String() != a.ReservationID || de != nil || len(digest) != 32 || a.NodeID != s.nodeID || a.Instance == "" || a.Epoch <= 0 || a.Action != action {
		return errors.New("invalid elastic authority")
	}
	if s.authorizeElastic == nil || s.elasticFenced {
		return errors.New("elastic protocol unavailable")
	}
	return s.authorizeElastic(ctx, a)
}

func elasticReply(w http.ResponseWriter, j *elasticJournal) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(j.Result)
}

func (s *replicaServer) handleElasticPrepare(w http.ResponseWriter, r *http.Request) {
	var req api.ElasticPrepareRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	s.elasticMu.Lock()
	defer s.elasticMu.Unlock()
	a := req.Authority
	if err := s.elasticAuthorized(r.Context(), a, "prepare"); err != nil {
		http.Error(w, "elastic authorization refused", 409)
		return
	}
	cap, ok := s.runtime.(process.GuardedStartCapable)
	_, canList := s.runtime.(containerLister)
	_, canRemove := s.runtime.(interface{ RemoveHandle(process.RunHandle) error })
	if !ok || !cap.SupportsGuardedStart() || !canList || !canRemove {
		http.Error(w, "guarded container runtime required", 422)
		return
	}
	if api.ElasticRequestDigest(req.Replica) != a.RequestDigest || req.Replica.Index < 0 || req.Replica.DeploymentID <= 0 {
		http.Error(w, "launch binding mismatch", 409)
		return
	}
	j, err := s.readElasticJournal(a.ReservationID)
	if err == nil {
		if j.Result.RequestDigest != a.RequestDigest || (j.Result.State != "prepared" && j.Result.State != "ready") {
			http.Error(w, "launch needs reconciliation", 409)
			return
		}
		elasticReply(w, j)
		return
	}
	if !os.IsNotExist(err) {
		http.Error(w, "cannot read launch journal", 503)
		return
	}
	token, err := newToken()
	if err != nil {
		http.Error(w, "cannot allocate token", 503)
		return
	}
	j = &elasticJournal{Result: api.ElasticResult{ReservationID: a.ReservationID, RequestDigest: a.RequestDigest, State: "preparing", Replica: api.ReplicaResult{NodeID: s.nodeID}}, Token: token, HostPort: s.allocatePort(), MaxSessions: req.Replica.MaxSessions}
	if err := s.saveElasticJournal(j); err != nil {
		http.Error(w, "cannot record launch intent", 503)
		return
	}
	// A restored or lost worker data directory must not make an existing
	// Docker incarnation invisible to replay protection.
	items, err := s.runtime.(containerLister).ListByLabel(`{}`)
	if err != nil {
		http.Error(w, "cannot establish prior launch absence", 503)
		return
	}
	for _, item := range items {
		if item.Labels[process.LabelLaunchID] == a.ReservationID {
			http.Error(w, "existing launch requires reconciliation", 409)
			return
		}
	}
	params, err := s.buildStartParams(r.Context(), req.Replica, j.HostPort)
	if err != nil {
		http.Error(w, "cannot prepare launch", 503)
		return
	}
	if err := s.elasticAuthorized(r.Context(), a, "prepare"); err != nil {
		http.Error(w, "launch authority expired", 409)
		return
	}
	params.GuardUntilAcknowledged = true
	params.LaunchID = a.ReservationID
	ep, err := s.runtime.Start(r.Context(), params, io.Discard)
	if err != nil {
		http.Error(w, "guarded launch failed; reconcile before retrying", 503)
		return
	}
	if ep.StartupGuard == nil || ep.Handle.ContainerID == "" {
		if ep.StartupGuard != nil {
			_ = ep.StartupGuard.Close()
		}
		http.Error(w, "runtime violated guarded launch contract", 503)
		return
	}
	s.elasticGuards[a.ReservationID] = ep.StartupGuard
	j.Result.Replica.ContainerID = ep.Handle.ContainerID
	j.Result.Replica.URL = fmt.Sprintf("https://%s/v1/data/%s", s.advertise, token)
	j.Result.State = "prepared"
	if err := s.saveElasticJournal(j); err != nil {
		http.Error(w, "cannot record runtime identity", 503)
		return
	}
	elasticReply(w, j)
}

func (s *replicaServer) handleElasticAck(w http.ResponseWriter, r *http.Request) {
	var a api.ElasticAuthority
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&a); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	s.elasticMu.Lock()
	defer s.elasticMu.Unlock()
	if err := s.elasticAuthorized(r.Context(), a, "ack"); err != nil {
		http.Error(w, "elastic authorization refused", 409)
		return
	}
	j, err := s.readElasticJournal(a.ReservationID)
	if err != nil || j.Result.RequestDigest != a.RequestDigest || (j.Result.State != "prepared" && j.Result.State != "acknowledging" && j.Result.State != "ready") {
		http.Error(w, "launch needs reconciliation", 409)
		return
	}
	if j.Result.State == "ready" {
		s.mu.RLock()
		rec := s.byToken[j.Token]
		live := rec != nil && rec.containerID == j.Result.Replica.ContainerID
		s.mu.RUnlock()
		if !live {
			http.Error(w, "worker restarted; stop and replace this reservation", 409)
			return
		}
		elasticReply(w, j)
		return
	}
	if j.Result.State != "ready" {
		guard := s.elasticGuards[a.ReservationID]
		if guard == nil {
			http.Error(w, "worker restarted; stop and replace this reservation", 409)
			return
		}
		j.Result.State = "acknowledging"
		if err := s.saveElasticJournal(j); err != nil {
			http.Error(w, "cannot record acknowledgement", 503)
			return
		}
		if _, err := guard.Write([]byte("ready\n")); err != nil {
			http.Error(w, "acknowledgement failed; reconcile launch", 503)
			return
		}
		if err := guard.Close(); err != nil {
			http.Error(w, "runtime start failed; reconcile launch", 503)
			return
		}
		delete(s.elasticGuards, a.ReservationID)
		j.Result.State = "ready"
		if err := s.saveElasticJournal(j); err != nil {
			http.Error(w, "cannot record running launch", 503)
			return
		}
	}
	// Elastic containers never enter legacy container-control maps: those
	// endpoints have no reservation/epoch authorization. Only the data token is
	// exposed, and only following a successful acknowledgement.
	s.mu.Lock()
	rec := &replicaRecord{token: j.Token, containerID: j.Result.Replica.ContainerID, hostPort: j.HostPort, maxSessions: j.MaxSessions}
	s.byToken[j.Token] = rec
	s.mu.Unlock()
	go func() {
		err := s.runtime.Wait(context.Background(), process.RunHandle{ContainerID: rec.containerID})
		// Even an uncertain runtime response withdraws routing. It never frees
		// capacity or removes the durable identity: stop still needs proof.
		s.mu.Lock()
		if s.byToken[rec.token] == rec {
			delete(s.byToken, rec.token)
		}
		s.mu.Unlock()
		if err != nil {
			slog.Warn("elastic worker: exit observation failed", "reservation", a.ReservationID, "err", err)
		}
	}()
	elasticReply(w, j)
}

func (s *replicaServer) handleElasticStop(w http.ResponseWriter, r *http.Request) {
	var a api.ElasticAuthority
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&a); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	s.elasticMu.Lock()
	defer s.elasticMu.Unlock()
	if err := s.elasticAuthorized(r.Context(), a, "stop"); err != nil {
		http.Error(w, "elastic authorization refused", 409)
		return
	}
	j, err := s.readElasticJournal(a.ReservationID)
	if os.IsNotExist(err) {
		j = &elasticJournal{Result: api.ElasticResult{ReservationID: a.ReservationID, RequestDigest: a.RequestDigest, State: "stopping", Replica: api.ReplicaResult{NodeID: s.nodeID}}}
	} else if err != nil {
		http.Error(w, "cannot read launch journal", 503)
		return
	}
	if j.Result.RequestDigest != a.RequestDigest {
		http.Error(w, "launch binding mismatch", 409)
		return
	}
	if err := s.stopElastic(j); err != nil {
		http.Error(w, "worker termination unconfirmed", 503)
		return
	}
	elasticReply(w, j)
}

func (s *replicaServer) stopElastic(j *elasticJournal) error {
	id := j.Result.ReservationID
	if j.Result.State == "stopped" {
		return nil
	}
	j.Result.State = "stopping"
	if err := s.saveElasticJournal(j); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.byToken, j.Token)
	s.mu.Unlock()
	if guard := s.elasticGuards[id]; guard != nil {
		_ = guard.Close()
		delete(s.elasticGuards, id)
	}
	lister, ok := s.runtime.(containerLister)
	remover, removable := s.runtime.(interface{ RemoveHandle(process.RunHandle) error })
	if !ok || !removable {
		return errors.New("runtime cannot prove removal")
	}
	filter := []byte(`{}`)
	items, err := lister.ListByLabel(string(filter))
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Labels[process.LabelLaunchID] != id {
			if item.ID == j.Result.Replica.ContainerID {
				return errors.New("recorded container has mismatched launch identity")
			}
			continue
		}
		if err := remover.RemoveHandle(process.RunHandle{ContainerID: item.ID}); err != nil {
			return err
		}
	}
	items, err = lister.ListByLabel(string(filter))
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == j.Result.Replica.ContainerID || item.Labels[process.LabelLaunchID] == id {
			return errors.New("launch survivors remain")
		}
	}
	j.Result.State = "stopped"
	return s.saveElasticJournal(j)
}

func (s *replicaServer) fenceElastic() {
	s.elasticMu.Lock()
	defer s.elasticMu.Unlock()
	s.elasticFenced = true
	paths, err := filepath.Glob(filepath.Join(s.dataDir, "elastic-launches", "*.json"))
	if err != nil {
		return
	}
	for _, path := range paths {
		id := filepath.Base(path)
		id = id[:len(id)-len(".json")]
		j, err := s.readElasticJournal(id)
		if err == nil {
			err = s.stopElastic(j)
		}
		if err != nil {
			slog.Error("elastic fence: cleanup unconfirmed", "reservation", id, "err", err)
		}
	}
}

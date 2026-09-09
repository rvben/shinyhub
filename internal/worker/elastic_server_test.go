package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// This runtime distinguishes container creation from application execution.
// A guard closed without ready aborts, as required of production runtimes.
type elasticTestRuntime struct {
	onStart  func()
	exited   chan struct{}
	exitOnce sync.Once
	fakeRuntime
	starts, executions int
	containers         map[string]process.ContainerInfo
	listErr, removeErr error
	retainRemoved      bool
}
type elasticTestGuard struct {
	runtime       *elasticTestRuntime
	ready, closed bool
}

func (g *elasticTestGuard) Write(p []byte) (int, error) {
	if g.closed {
		return 0, errors.New("closed")
	}
	g.ready = string(p) == "ready\n"
	return len(p), nil
}
func (g *elasticTestGuard) Close() error {
	if !g.closed && g.ready {
		g.runtime.executions++
	}
	g.closed = true
	return nil
}
func (f *elasticTestRuntime) Wait(ctx context.Context, _ process.RunHandle) error {
	select {
	case <-f.exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *elasticTestRuntime) SupportsGuardedStart() bool { return true }
func (f *elasticTestRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	if !p.GuardUntilAcknowledged || p.LaunchID == "" {
		return process.ReplicaEndpoint{}, errors.New("unguarded start")
	}
	if f.exited == nil {
		f.exited = make(chan struct{})
	}
	f.startParams = p
	f.starts++
	id := fmt.Sprintf("elastic-%d", f.starts)
	f.containers[id] = process.ContainerInfo{ID: id, Labels: map[string]string{process.LabelLaunchID: p.LaunchID, "shinyhub.replica_index": "0", "shinyhub.managed": "true"}}
	if f.onStart != nil {
		f.onStart()
	}
	return process.ReplicaEndpoint{Handle: process.RunHandle{ContainerID: id}, StartupGuard: &elasticTestGuard{runtime: f}}, nil
}
func (f *elasticTestRuntime) ListByLabel(filter string) ([]process.ContainerInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var parsed map[string][]string
	if err := json.Unmarshal([]byte(filter), &parsed); err != nil {
		return nil, err
	}
	var out []process.ContainerInfo
	for _, c := range f.containers {
		matches := true
		for _, label := range parsed["label"] {
			k, v, hasValue := strings.Cut(label, "=")
			if actual, ok := c.Labels[k]; !ok || (hasValue && actual != v) {
				matches = false
			}
		}
		if matches {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *elasticTestRuntime) RemoveHandle(h process.RunHandle) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	if !f.retainRemoved {
		delete(f.containers, h.ContainerID)
		f.exitOnce.Do(func() { close(f.exited) })
	}
	return nil
}
func elasticFixture(t *testing.T) (*replicaServer, *elasticTestRuntime, api.ElasticPrepareRequest, *int64) {
	t.Helper()
	rt := &elasticTestRuntime{containers: make(map[string]process.ContainerInfo)}
	epoch := int64(1)
	srv := NewReplicaServer(ReplicaServerConfig{Runtime: rt, DataDir: t.TempDir(), NodeID: "node-a", Advertise: "worker:8443", AllocatePort: func() int { return 49001 }, AuthorizeElastic: func(_ context.Context, a api.ElasticAuthority) error {
		if a.Epoch != epoch {
			return errors.New("stale controller")
		}
		return nil
	}})
	t.Cleanup(srv.fenceElastic)
	replica := api.ReplicaStartRequest{Slug: "app", Index: 0, DeploymentID: 7, Command: []string{"./server"}, BindPort: 8080}
	req := api.ElasticPrepareRequest{Replica: replica, Authority: api.ElasticAuthority{ReservationID: uuid.NewString(), NodeID: "node-a", Instance: "controller", Epoch: 1, RequestDigest: api.ElasticRequestDigest(replica), Action: "prepare"}}
	return srv, rt, req, &epoch
}
func elasticCall(t *testing.T, handler http.HandlerFunc, body any, status int) api.ElasticResult {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/v1/elastic", bytes.NewReader(data)))
	if rec.Code != status {
		t.Fatalf("status=%d want=%d: %s", rec.Code, status, rec.Body.String())
	}
	var result api.ElasticResult
	if status == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
func elasticAction(req api.ElasticPrepareRequest, action string) api.ElasticAuthority {
	a := req.Authority
	a.Action = action
	return a
}

func TestElasticPrepareAckIdempotentAndPrivate(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	first := elasticCall(t, srv.handleElasticPrepare, req, 200)
	again := elasticCall(t, srv.handleElasticPrepare, req, 200)
	if first != again || first.State != "prepared" || rt.starts != 1 || rt.executions != 0 {
		t.Fatalf("prepare replay=%+v first=%+v starts=%d executions=%d", again, first, rt.starts, rt.executions)
	}
	if len(srv.byToken) != 0 || len(srv.byContainer) != 0 {
		t.Fatal("unacknowledged worker exposed")
	}
	changed := req
	changed.Replica.Command = []string{"./different"}
	elasticCall(t, srv.handleElasticPrepare, changed, 409)
	changed.Authority.RequestDigest = api.ElasticRequestDigest(changed.Replica)
	elasticCall(t, srv.handleElasticPrepare, changed, 409)
	ready := elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
	replay := elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
	if ready != replay || ready.State != "ready" || rt.starts != 1 || rt.executions != 1 {
		t.Fatalf("ack replay changed launch: %+v %+v starts=%d executions=%d", ready, replay, rt.starts, rt.executions)
	}
	if len(srv.byToken) != 1 || len(srv.byContainer) != 0 {
		t.Fatal("ack must expose data token but never legacy controls")
	}
}

func TestElasticStopTombstoneSurvivesRestart(t *testing.T) {
	for _, prepare := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared=%v", prepare), func(t *testing.T) {
			srv, rt, req, _ := elasticFixture(t)
			if prepare {
				elasticCall(t, srv.handleElasticPrepare, req, 200)
			}
			stopped := elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 200)
			if stopped.State != "stopped" || len(rt.containers) != 0 || rt.executions != 0 {
				t.Fatal("stop did not abort and remove guarded launch")
			}
			restart := NewReplicaServer(ReplicaServerConfig{Runtime: rt, DataDir: srv.dataDir, NodeID: srv.nodeID, AuthorizeElastic: srv.authorizeElastic})
			elasticCall(t, restart.handleElasticPrepare, req, 409)
			elasticCall(t, restart.handleElasticAck, elasticAction(req, "ack"), 409)
			replay := elasticCall(t, restart.handleElasticStop, elasticAction(req, "stop"), 200)
			if replay != stopped {
				t.Fatalf("tombstone changed: %+v %+v", stopped, replay)
			}
		})
	}
}

func TestElasticStaleAuthorityCannotExecuteOrStop(t *testing.T) {
	srv, rt, req, epoch := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	*epoch = 2
	elasticCall(t, srv.handleElasticPrepare, req, 409)
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 409)
	elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 409)
	if rt.executions != 0 || len(rt.containers) != 1 {
		t.Fatal("stale controller mutated launch")
	}
	current := elasticAction(req, "stop")
	current.Epoch = 2
	elasticCall(t, srv.handleElasticStop, current, 200)
}

func TestElasticStopRequiresVerifiedRemoval(t *testing.T) {
	for _, failure := range []string{"inventory", "remove", "survivor"} {
		t.Run(failure, func(t *testing.T) {
			srv, rt, req, _ := elasticFixture(t)
			elasticCall(t, srv.handleElasticPrepare, req, 200)
			elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
			switch failure {
			case "inventory":
				rt.listErr = errors.New("unreachable")
			case "remove":
				rt.removeErr = errors.New("unreachable")
			case "survivor":
				rt.retainRemoved = true
			}
			elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 503)
			journal, err := srv.readElasticJournal(req.Authority.ReservationID)
			if err != nil {
				t.Fatal(err)
			}
			if journal.Result.State == "stopped" || len(srv.byToken) != 0 {
				t.Fatal("unconfirmed termination marked stopped or still routable")
			}
			rt.listErr = nil
			rt.removeErr = nil
			rt.retainRemoved = false
			result := elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 200)
			if result.State != "stopped" || len(rt.containers) != 0 {
				t.Fatal("stop retry failed")
			}
		})
	}
}

func TestElasticRestartCannotAckPreparedOrReadoptThroughLegacy(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	restart := NewReplicaServer(ReplicaServerConfig{Runtime: rt, DataDir: srv.dataDir, NodeID: srv.nodeID, AuthorizeElastic: srv.authorizeElastic})
	if err := restart.RebuildFromContainers(); err != nil {
		t.Fatal(err)
	}
	if len(restart.byToken) != 0 || len(restart.byContainer) != 0 {
		t.Fatal("elastic launch readopted into legacy controls")
	}
	elasticCall(t, restart.handleElasticAck, elasticAction(req, "ack"), 409)
	if rt.executions != 0 {
		t.Fatal("restarted worker executed without live guard")
	}
	elasticCall(t, restart.handleElasticStop, elasticAction(req, "stop"), 200)
}

func TestElasticDisabledAndFenced(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	authorize := srv.authorizeElastic
	srv.authorizeElastic = nil
	elasticCall(t, srv.handleElasticPrepare, req, 409)
	srv.authorizeElastic = authorize
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	srv.fenceElastic()
	elasticCall(t, srv.handleElasticPrepare, req, 409)
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 409)
	if rt.executions != 0 || len(rt.containers) != 0 {
		t.Fatal("fencing failed to abort guarded worker")
	}
}

func TestElasticDuplicateAckPreservesConnectionAccounting(t *testing.T) {
	srv, _, req, _ := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
	var token string
	var original *replicaRecord
	for key, record := range srv.byToken {
		token = key
		original = record
	}
	original.activeConns.Store(3)
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
	if srv.byToken[token] != original || srv.byToken[token].activeConns.Load() != 3 {
		t.Fatal("replayed ack reset active connection accounting")
	}
}

func TestElasticRestartCannotRouteUnverifiedReadyJournal(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
	restart := NewReplicaServer(ReplicaServerConfig{Runtime: rt, DataDir: srv.dataDir, NodeID: srv.nodeID, AuthorizeElastic: srv.authorizeElastic})
	elasticCall(t, restart.handleElasticAck, elasticAction(req, "ack"), 409)
	if len(restart.byToken) != 0 || rt.executions != 1 {
		t.Fatal("restarted worker routed unchecked journal or re-executed")
	}
	elasticCall(t, restart.handleElasticStop, elasticAction(req, "stop"), 200)
}

// Holding the acknowledgement inside the runtime makes the ack/stop overlap
// deterministic. The stop must remove the released worker and its data route.
type elasticPausedGuard struct {
	io.WriteCloser
	entered, release chan struct{}
}

func (g *elasticPausedGuard) Close() error {
	close(g.entered)
	<-g.release
	return g.WriteCloser.Close()
}
func TestElasticConcurrentAckStopLeavesNoWorkerOrRoute(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	guard := &elasticPausedGuard{WriteCloser: srv.elasticGuards[req.Authority.ReservationID], entered: make(chan struct{}), release: make(chan struct{})}
	srv.elasticGuards[req.Authority.ReservationID] = guard
	ack := httptest.NewRecorder()
	ackBody, _ := json.Marshal(elasticAction(req, "ack"))
	ackDone := make(chan struct{})
	go func() {
		defer close(ackDone)
		srv.handleElasticAck(ack, httptest.NewRequest(http.MethodPost, "/v1/elastic/ack", bytes.NewReader(ackBody)))
	}()
	select {
	case <-guard.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("ack did not reach runtime guard")
	}
	stop := httptest.NewRecorder()
	stopBody, _ := json.Marshal(elasticAction(req, "stop"))
	stopStarted, stopDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopDone)
		close(stopStarted)
		srv.handleElasticStop(stop, httptest.NewRequest(http.MethodPost, "/v1/elastic/stop", bytes.NewReader(stopBody)))
	}()
	<-stopStarted
	close(guard.release)
	select {
	case <-ackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("ack stuck")
	}
	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("stop stuck")
	}
	if ack.Code != 200 || stop.Code != 200 {
		t.Fatalf("ack=%d stop=%d", ack.Code, stop.Code)
	}
	if rt.starts != 1 || rt.executions != 1 || len(rt.containers) != 0 || len(srv.byToken) != 0 {
		t.Fatal("concurrent stop failed to terminate acknowledged launch")
	}
	journal, err := srv.readElasticJournal(req.Authority.ReservationID)
	if err != nil || journal.Result.State != "stopped" {
		t.Fatalf("journal=%+v err=%v", journal, err)
	}
}

func TestElasticStopDoesNotRemoveDifferentLaunch(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	result := elasticCall(t, srv.handleElasticPrepare, req, 200)
	rt.containers["unrelated"] = process.ContainerInfo{ID: "unrelated", Labels: map[string]string{process.LabelLaunchID: uuid.NewString()}}
	c := rt.containers[result.Replica.ContainerID]
	c.Labels[process.LabelLaunchID] = uuid.NewString()
	rt.containers[c.ID] = c
	elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 503)
	if len(rt.containers) != 2 {
		t.Fatal("stop removed a container with another launch identity")
	}
	c.Labels[process.LabelLaunchID] = req.Authority.ReservationID
	rt.containers[c.ID] = c
	elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 200)
	if _, ok := rt.containers["unrelated"]; !ok {
		t.Fatal("stop removed unrelated launch")
	}
}

func TestElasticRuntimeExitRevokesRouteWithoutReexecution(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 200)
	rt.exitOnce.Do(func() { close(rt.exited) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		srv.mu.RLock()
		routes := len(srv.byToken)
		srv.mu.RUnlock()
		if routes == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("exited worker retained its route")
		case <-tick.C:
		}
	}
	elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 409)
	if rt.executions != 1 || rt.starts != 1 {
		t.Fatal("exited launch was re-executed")
	}
	journal, err := srv.readElasticJournal(req.Authority.ReservationID)
	if err != nil || journal.Result.State == "stopped" {
		t.Fatalf("exit alone confirmed removal: journal=%+v err=%v", journal, err)
	}
	elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 200)
}

func TestElasticJournalFailureNeverExecutesOrDuplicates(t *testing.T) {
	t.Run("before creation", func(t *testing.T) {
		srv, rt, req, _ := elasticFixture(t)
		blocked := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		srv.dataDir = blocked
		elasticCall(t, srv.handleElasticPrepare, req, 503)
		if rt.starts != 0 || rt.executions != 0 {
			t.Fatal("runtime created without durable intent")
		}
	})
	t.Run("after creation", func(t *testing.T) {
		srv, rt, req, _ := elasticFixture(t)
		path := srv.journalPath(req.Authority.ReservationID)
		var intent []byte
		rt.onStart = func() {
			var err error
			intent, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		elasticCall(t, srv.handleElasticPrepare, req, 503)
		if rt.starts != 1 || rt.executions != 0 {
			t.Fatal("identity persistence failure executed launch")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, intent, 0600); err != nil {
			t.Fatal(err)
		}
		rt.onStart = nil
		elasticCall(t, srv.handleElasticPrepare, req, 409)
		elasticCall(t, srv.handleElasticAck, elasticAction(req, "ack"), 409)
		if rt.starts != 1 || rt.executions != 0 {
			t.Fatal("incomplete launch retry duplicated or executed")
		}
		elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 200)
		if len(rt.containers) != 0 {
			t.Fatal("reconciliation did not remove unidentified launch")
		}
	})
}

func TestElasticLostJournalCannotDuplicateSurvivingLaunch(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	elasticCall(t, srv.handleElasticPrepare, req, 200)
	if err := os.Remove(srv.journalPath(req.Authority.ReservationID)); err != nil {
		t.Fatal(err)
	}
	restart := NewReplicaServer(ReplicaServerConfig{Runtime: rt, DataDir: srv.dataDir, NodeID: srv.nodeID, AuthorizeElastic: srv.authorizeElastic, AllocatePort: func() int { return 49002 }})
	t.Cleanup(restart.fenceElastic)
	elasticCall(t, restart.handleElasticPrepare, req, 409)
	if rt.starts != 1 || rt.executions != 0 || len(rt.containers) != 1 {
		t.Fatal("lost journal allowed a duplicate launch")
	}
	elasticCall(t, restart.handleElasticStop, elasticAction(req, "stop"), 200)
	if len(rt.containers) != 0 {
		t.Fatal("lost journal prevented surviving launch reconciliation")
	}
}

func TestElasticPrepareRequiresAvailableInventory(t *testing.T) {
	srv, rt, req, _ := elasticFixture(t)
	rt.listErr = errors.New("daemon inventory unavailable")
	elasticCall(t, srv.handleElasticPrepare, req, 503)
	if rt.starts != 0 || rt.executions != 0 {
		t.Fatal("launch created without reliable duplicate inventory")
	}
	rt.listErr = nil
	elasticCall(t, srv.handleElasticStop, elasticAction(req, "stop"), 200)
}

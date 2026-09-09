package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/worker/api"
)

// ElasticWorkerDialer is satisfied by the existing mTLS worker Dialer.
type ElasticWorkerDialer interface {
	DialWorker(db.Worker) (*http.Client, string, error)
}

// ElasticLauncher coordinates the opt-in protocol. Callers must retain the
// selected worker across retries; the database binding enforces this. It is not
// wired into public routing until the shared proxy admission path is complete.
type ElasticLauncher struct {
	Store  *db.Store
	Dialer ElasticWorkerDialer
}

func (c *ElasticLauncher) exchange(ctx context.Context, worker db.Worker, a api.ElasticAuthority, body any) (*api.ElasticResult, error) {
	client, base, err := c.Dialer.DialWorker(worker)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/elastic/"+a.Action, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "shinyhub-elastic")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elastic %s returned HTTP %d; reservation retained", a.Action, resp.StatusCode)
	}
	var result api.ElasticResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if result.ReservationID != a.ReservationID || result.RequestDigest != a.RequestDigest || result.Replica.NodeID != worker.NodeID {
		return nil, fmt.Errorf("worker returned a different launch identity; reservation retained")
	}
	return &result, nil
}

// Launch prepares and acknowledges execution. "ready" is protocol completion,
// not an application readiness probe; callers must probe before routing.
func (c *ElasticLauncher) Launch(ctx context.Context, owner db.ElasticOwner, reservation db.ElasticReservation, worker db.Worker, request api.ReplicaStartRequest) (*api.ElasticResult, error) {
	current, err := c.Store.GetElasticReservation(ctx, reservation.ID)
	if err != nil {
		return nil, err
	}
	reservation = *current
	app, err := c.Store.GetAppByID(reservation.AppID)
	if err != nil {
		return nil, err
	}
	if request.Slug != app.Slug || request.Index != reservation.Slot || request.DeploymentID != reservation.DeploymentID {
		return nil, db.ErrElasticConflict
	}
	a := api.ElasticAuthority{ReservationID: reservation.ID, NodeID: worker.NodeID, Instance: owner.Instance, Epoch: owner.Epoch, RequestDigest: api.ElasticRequestDigest(request), Action: api.ElasticPrepare}
	if err := c.Store.BindElasticLaunch(ctx, owner, a.ReservationID, a.NodeID, a.RequestDigest); err != nil {
		return nil, err
	}
	prepared, err := c.exchange(ctx, worker, a, api.ElasticPrepareRequest{Authority: a, Replica: request})
	if err != nil {
		return nil, err
	}
	if (prepared.State != "prepared" && prepared.State != "ready") || prepared.Replica.ContainerID == "" {
		return nil, fmt.Errorf("worker did not prepare a runtime identity")
	}
	a.Action = api.ElasticAck
	if err := c.Store.AuthorizeElasticCommand(ctx, owner, a.ReservationID, a.NodeID, a.RequestDigest, a.Action); err != nil {
		return nil, err
	}
	ready, err := c.exchange(ctx, worker, a, a)
	if err != nil {
		return nil, err
	}
	if ready.State != "ready" || ready.Replica != prepared.Replica {
		return nil, fmt.Errorf("worker acknowledgement identity mismatch")
	}
	if err := c.Store.AdvanceElasticReservation(ctx, owner, a.ReservationID, "ready"); err != nil {
		return nil, err
	}
	return ready, nil
}

// Stop retains capacity on any transport, inventory, or persistence error.
// Neither a missing worker nor HTTP 404 is proof that the worker stopped.
func (c *ElasticLauncher) Stop(ctx context.Context, owner db.ElasticOwner, reservationID string, worker db.Worker, requestDigest string) error {
	if err := c.Store.StopElasticReservation(ctx, owner, reservationID); err != nil {
		return err
	}
	a := api.ElasticAuthority{ReservationID: reservationID, NodeID: worker.NodeID, Instance: owner.Instance, Epoch: owner.Epoch, RequestDigest: requestDigest, Action: api.ElasticStop}
	if err := c.Store.AuthorizeElasticCommand(ctx, owner, a.ReservationID, a.NodeID, a.RequestDigest, a.Action); err != nil {
		return err
	}
	result, err := c.exchange(ctx, worker, a, a)
	if err != nil {
		return err
	}
	if result.State != "stopped" {
		return fmt.Errorf("worker termination unconfirmed; reservation retained")
	}
	return c.Store.ConfirmElasticReservationStopped(ctx, owner, reservationID)
}

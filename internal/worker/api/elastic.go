package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	ElasticPrepare = "prepare"
	ElasticAck     = "ack"
	ElasticStop    = "stop"
)

// ElasticAuthority binds one command to the exact reservation and worker.
// Transport authentication alone does not establish controller ownership.
type ElasticAuthority struct {
	ReservationID string `json:"reservation_id"`
	NodeID        string `json:"node_id"`
	Instance      string `json:"instance"`
	Epoch         int64  `json:"epoch"`
	RequestDigest string `json:"request_digest"`
	Action        string `json:"action"`
}

type ElasticPrepareRequest struct {
	Authority ElasticAuthority    `json:"authority"`
	Replica   ReplicaStartRequest `json:"replica"`
}

type ElasticResult struct {
	ReservationID string        `json:"reservation_id"`
	RequestDigest string        `json:"request_digest"`
	State         string        `json:"state"`
	Replica       ReplicaResult `json:"replica"`
}

// ElasticRequestDigest includes execution settings and environment, but only
// the digest belongs in durable journals. encoding/json sorts map keys.
func ElasticRequestDigest(r ReplicaStartRequest) string {
	b, _ := json.Marshal(r)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

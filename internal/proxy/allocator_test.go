package proxy

import (
	"testing"

	"github.com/rvben/shinyhub/internal/config"
)

func TestDecide(t *testing.T) {
	W := func(id, clients int) workerState {
		return workerState{slotID: id, assignedClients: clients, status: workerRunning}
	}
	cases := []struct {
		name       string
		workers    []workerState
		mode       config.WorkerIsolationMode
		grouped    int
		maxWorkers int
		pinned     int
		want       decision
	}{
		{"pin routes", []workerState{W(3, 1)}, config.IsolationPerSession, 1, 5, 3, decision{decisionRoute, 3}},
		{"per_session allocate", nil, config.IsolationPerSession, 1, 5, -1, decision{kind: decisionAllocate}},
		{"per_session full rejects", []workerState{W(0, 1), W(1, 1)}, config.IsolationPerSession, 1, 2, -1, decision{kind: decisionReject}},
		{"grouped packs", []workerState{W(0, 1), W(1, 7)}, config.IsolationGrouped, 8, 5, -1, decision{decisionRoute, 1}},
		{"grouped all full allocate", []workerState{W(0, 8)}, config.IsolationGrouped, 8, 5, -1, decision{kind: decisionAllocate}},
		{"grouped full at max rejects", []workerState{W(0, 8), W(1, 8)}, config.IsolationGrouped, 8, 2, -1, decision{kind: decisionReject}},
		{"stale pin allocates", []workerState{W(0, 1)}, config.IsolationPerSession, 1, 5, 99, decision{kind: decisionAllocate}},
		{"grouped binds a new client to a booting slot under cap", []workerState{{slotID: 0, assignedClients: 1, status: workerBooting}}, config.IsolationGrouped, 8, 5, -1, decision{decisionBind, 0}},
		{"grouped prefers a ready worker over a booting one", []workerState{W(0, 1), {slotID: 1, assignedClients: 1, status: workerBooting}}, config.IsolationGrouped, 8, 5, -1, decision{decisionRoute, 0}},
		{"grouped packs the fullest booting slot", []workerState{{slotID: 0, assignedClients: 1, status: workerBooting}, {slotID: 1, assignedClients: 3, status: workerBooting}}, config.IsolationGrouped, 8, 5, -1, decision{decisionBind, 1}},
		{"grouped booting slot at cap allocates", []workerState{{slotID: 0, assignedClients: 8, status: workerBooting}}, config.IsolationGrouped, 8, 5, -1, decision{kind: decisionAllocate}},
		{"grouped booting slots at cap at max rejects", []workerState{{slotID: 0, assignedClients: 8, status: workerBooting}, {slotID: 1, assignedClients: 8, status: workerBooting}}, config.IsolationGrouped, 8, 2, -1, decision{kind: decisionReject}},
		{"per_session booting slot is full for new clients", []workerState{{slotID: 0, assignedClients: 1, status: workerBooting}}, config.IsolationPerSession, 1, 5, -1, decision{kind: decisionAllocate}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(tc.workers, tc.mode, tc.grouped, tc.maxWorkers, tc.pinned); got != tc.want {
				t.Fatalf("decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

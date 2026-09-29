package proxy

import (
	"sync"
	"time"
)

const (
	wsBurstWindow    = 10 * time.Second
	wsBurstCooldown  = 30 * time.Second
	wsBurstThreshold = 3
)

// WSAbnormalBurst reports a cluster of abnormal endings observed by this proxy
// instance. It does not diagnose a CPU stall or any other root cause.
type WSAbnormalBurst struct {
	Slug         string
	Replica      int
	DeploymentID int64
	Count        int
	Window       time.Duration
	Span         time.Duration
	CloseCode    *uint16
	CloseReason  string
}

type wsBurstKey struct {
	slug       string
	replica    int
	deployment int64
}

type wsBurstState struct {
	ends     []wsBurstEnd
	lastWarn time.Time
}

type wsBurstEnd struct {
	at     time.Time
	code   *uint16
	reason string
}

// WSAbnormalBurstDetector is a process-local, bounded-window detector. The
// slot and deployment together identify one worker generation. Entries are
// swept lazily so no background goroutine is needed.
type WSAbnormalBurstDetector struct {
	mu        sync.Mutex
	now       func() time.Time
	states    map[wsBurstKey]*wsBurstState
	lastSweep time.Time
}

func NewWSAbnormalBurstDetector() *WSAbnormalBurstDetector {
	return &WSAbnormalBurstDetector{now: time.Now, states: make(map[wsBurstKey]*wsBurstState)}
}

func (d *WSAbnormalBurstDetector) Record(e WSSessionEnd) (WSAbnormalBurst, bool) {
	if !e.Abnormal || e.ReplicaIndex < 0 {
		return WSAbnormalBurst{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if d.lastSweep.IsZero() || now.Sub(d.lastSweep) >= time.Minute {
		for key, state := range d.states {
			if len(state.ends) == 0 || now.Sub(state.ends[len(state.ends)-1].at) > wsBurstWindow+wsBurstCooldown {
				delete(d.states, key)
			}
		}
		d.lastSweep = now
	}
	key := wsBurstKey{slug: e.Slug, replica: e.ReplicaIndex, deployment: e.DeploymentID}
	state := d.states[key]
	if state == nil {
		state = &wsBurstState{}
		d.states[key] = state
	}
	keep := state.ends[:0]
	for _, end := range state.ends {
		if now.Sub(end.at) <= wsBurstWindow {
			keep = append(keep, end)
		}
	}
	state.ends = append(keep, wsBurstEnd{at: now, code: e.CloseCode, reason: e.CloseReason})
	if len(state.ends) < wsBurstThreshold || !state.lastWarn.IsZero() && now.Sub(state.lastWarn) < wsBurstCooldown {
		return WSAbnormalBurst{}, false
	}
	state.lastWarn = now
	var code *uint16
	reason := state.ends[0].reason
	if state.ends[0].code != nil {
		copyCode := *state.ends[0].code
		code = &copyCode
	}
	for _, end := range state.ends[1:] {
		if (code == nil) != (end.code == nil) || code != nil && *code != *end.code || reason != end.reason {
			code, reason = nil, ""
			break
		}
	}
	return WSAbnormalBurst{
		Slug: e.Slug, Replica: e.ReplicaIndex, DeploymentID: e.DeploymentID,
		Count: len(state.ends), Window: wsBurstWindow, Span: now.Sub(state.ends[0].at),
		CloseCode: code, CloseReason: reason,
	}, true
}

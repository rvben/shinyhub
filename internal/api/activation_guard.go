package api

import (
	"errors"
	"fmt"
)

var errScheduleActivationInFlight = errors.New("scheduled data activation owns serving runtime state")
var errCompatibilityQuarantined = errors.New("app is compatibility-quarantined")

// guardActivationLifecycle is called while the app's deploy lock is held.
// The lock excludes an active Roll call; the durable query excludes mutations
// between repair attempts, when no goroutine owns the lock but the activation
// still owns runtime and routing state.
func (s *Server) guardActivationLifecycle(appID int64, operation string) error {
	inFlight, err := s.store.ScheduleActivationInFlight(appID)
	if err != nil {
		return fmt.Errorf("%s: check scheduled data activation: %w", operation, err)
	}
	if inFlight {
		return fmt.Errorf("%s: %w; retry after activation completes or repairs", operation, errScheduleActivationInFlight)
	}
	return nil
}

func (s *Server) guardCompatibilityQuarantine(appID int64, operation string) error {
	quarantined, err := s.store.AppCompatibilityQuarantined(appID)
	if err != nil {
		return fmt.Errorf("%s: check compatibility quarantine: %w", operation, err)
	}
	if quarantined {
		return fmt.Errorf("%s: %w; rerun every failed data producer successfully, or deploy a replacement producer policy that repairs the data, before starting consumers", operation, errCompatibilityQuarantined)
	}
	return nil
}

func (s *Server) acquireConsumerBootGate(appID int64) (func(), error) {
	if s.jobs == nil {
		quarantined, err := s.store.AppCompatibilityQuarantined(appID)
		if err != nil {
			return nil, err
		}
		if quarantined {
			return nil, errCompatibilityQuarantined
		}
		return func() {}, nil
	}
	return s.jobs.AcquireCompatibleConsumerBootGate(appID)
}

// acquireRawConsumerBootGate is reserved for candidate convergence, which
// must be able to enter the fence precisely in order to repair an existing
// quarantine before promotion.
func (s *Server) acquireRawConsumerBootGate(appID int64) (func(), error) {
	if s.jobs == nil {
		return func() {}, nil
	}
	return s.jobs.AcquireConsumerBootGate(appID)
}

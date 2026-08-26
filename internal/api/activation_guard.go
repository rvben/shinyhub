package api

import (
	"errors"
	"fmt"
)

var errScheduleActivationInFlight = errors.New("scheduled data activation owns serving runtime state")

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

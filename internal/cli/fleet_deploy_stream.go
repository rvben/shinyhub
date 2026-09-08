package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/deployevent"
	"github.com/rvben/shinyhub/internal/deployfail"
)

// Uncertain and post-publication failures must carry their mutation evidence
// independently of retryable failure kinds. A health-only retry cannot repair
// configuration that failed after publication.
type fleetDeployOutcomeError struct {
	err      error
	mutation applyMutationState
}

func (e *fleetDeployOutcomeError) Error() string { return e.err.Error() }
func (e *fleetDeployOutcomeError) Unwrap() error { return e.err }

// The fleet uses the existing negotiated protocol. Older servers still return
// ordinary JSON. Never replay a deploy merely because its event stream ended:
// the terminal result (and thus whether publication happened) is unknown.
func consumeFleetDeployEvents(resp *http.Response, slug string, out io.Writer) ([]byte, deployfail.Kind, error) {
	decoder := json.NewDecoder(resp.Body)
	reminders := waitUpdates{interval: 15 * time.Second}
	phase, recovery := "deployment", ""
	published := false
	outcomeError := func(err error) error {
		mutation := mutationUnknown
		if published {
			mutation = mutationPartial
		}
		return &fleetDeployOutcomeError{err: err, mutation: mutation}
	}
	for {
		var event deployevent.Event
		if err := decoder.Decode(&event); err != nil {
			return nil, deployfail.Unknown, outcomeError(fmt.Errorf("deploy %s: lost progress during %s; deployment outcome is unknown (%w); run: shinyhub apps show %s", slug, phase, err, shellQuote(slug)))
		}
		switch event.Type {
		case deployevent.TypePhase:
			if (event.Phase == "commit" || event.Phase == "handoff") && event.Status == deployevent.StatusCompleted {
				published = true
			}
			if event.Phase != "" {
				phase = liveText(event.Phase)
			}
			if event.Phase == "recovery" && event.Status == deployevent.StatusCompleted {
				recovery = liveText(event.Message)
			}
			message := liveText(event.Message)
			if message == "" {
				continue
			}
			if event.Status == deployevent.StatusWarning {
				emitFleetWarning(out, slug, message)
				continue
			}
			if updateFleetProgress(out, message, "", time.Time{}, event.Status == deployevent.StatusFailed) {
				continue
			}
			key := event.Phase + "/" + event.Status + "/" + message
			if reminders.due(time.Now(), key) {
				if event.ElapsedSeconds > 0 {
					message += fmt.Sprintf(" (%s elapsed)", humanElapsed(time.Duration(event.ElapsedSeconds)*time.Second))
				}
				fmt.Fprintf(out, "  %s: %s\n", slug, message)
			}
		case deployevent.TypeResult:
			var result map[string]json.RawMessage
			if len(event.Result) == 0 || json.Unmarshal(event.Result, &result) != nil || result == nil {
				return nil, deployfail.Unknown, outcomeError(fmt.Errorf("deploy %s: invalid deployment result; outcome is unknown; run: shinyhub apps show %s", slug, shellQuote(slug)))
			}
			return event.Result, "", nil
		case deployevent.TypeError:
			code := event.StatusCode
			if code < 400 {
				code = http.StatusInternalServerError
			}
			kind := deployfail.Kind(event.FailureKind)
			if !kind.Valid() {
				kind = deployfail.Unknown
				if code >= 500 {
					kind = deployfail.ServerError
				}
			}
			if event.Phase != "" {
				phase = liveText(event.Phase)
			}

			message := fmt.Sprintf("deploy %s failed during %s: %s", slug, phase, liveText(event.Message))
			if recovery != "" {
				message += "; " + recovery
			}
			command := fmt.Sprintf("shinyhub apps logs %s --system --tail 200", shellQuote(slug))
			uncertain := published || phase == "commit" || phase == "configuration"
			if published {
				message += "; deployment was already published"
			}
			if uncertain {
				command = fmt.Sprintf("shinyhub apps show %s", shellQuote(slug))
			}
			message += "; run: " + command
			var failure error = &httpStatusError{Status: code, msg: message}
			if uncertain {
				failure = outcomeError(failure)
			}
			return nil, kind, failure
		}
	}
}

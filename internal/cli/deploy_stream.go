package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/rvben/shinyhub/internal/deployevent"
)

func resolveDeployFormat() (outputFormat, error) {
	if outputFormat(outputFlagValue) == formatNDJSON {
		resolvedFormat = formatNDJSON
		return formatNDJSON, nil
	}
	return resolveFormat(false, false)
}

func isDeployEventResponse(resp *http.Response) bool {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && mediaType == deployevent.MediaType
}

type deployEventRenderer struct {
	w      io.Writer
	style  styler
	active *progress
	phase  string
}

func newDeployEventRenderer(w io.Writer) *deployEventRenderer {
	return &deployEventRenderer{w: w, style: stylerFor(w)}
}

func (r *deployEventRenderer) handle(event deployevent.Event) {
	if event.Type != deployevent.TypePhase {
		return
	}
	if !r.style.redraw {
		switch event.Status {
		case deployevent.StatusStarted:
			fmt.Fprintf(r.w, "  → %s\n", event.Message)
		case deployevent.StatusCompleted:
			fmt.Fprintf(r.w, "  %s%s\n", r.style.okPrefix(), event.Message)
		case deployevent.StatusWarning:
			fmt.Fprintf(r.w, "  ! %s\n", event.Message)
		case deployevent.StatusFailed:
			fmt.Fprintf(r.w, "  ✗ %s\n", event.Message)
		case deployevent.StatusProgress:
			// Heartbeats are valuable in CI, but replica transitions are already
			// concrete and should not be hidden behind a terminal-only spinner.
			if event.Replica != nil || event.ElapsedSeconds > 0 {
				fmt.Fprintf(r.w, "    %s\n", event.Message)
			}
		}
		return
	}

	switch event.Status {
	case deployevent.StatusStarted:
		if r.active != nil {
			r.active.stop()
		}
		r.phase = event.Phase
		r.active = newProgress(r.w, event.Message)
		r.active.start()
	case deployevent.StatusCompleted:
		if r.active != nil && r.phase == event.Phase {
			r.active.done("", event.Message)
			r.active = nil
		} else {
			fmt.Fprintf(r.w, "%s%s\n", r.style.okPrefix(), event.Message)
		}
	case deployevent.StatusWarning:
		if r.active != nil && r.phase == event.Phase {
			r.active.stop()
			r.active = nil
		}
		fmt.Fprintf(r.w, "! %s\n", event.Message)
	case deployevent.StatusFailed:
		r.stop()
	}
}

func (r *deployEventRenderer) stop() {
	if r.active != nil {
		r.active.stop()
		r.active = nil
	}
}

// consumeDeployEvents returns the original deploy response JSON carried by the
// terminal result event. Thus everything after the request keeps one response
// shape whether the server streamed or used the legacy single JSON document.
func consumeDeployEvents(resp *http.Response, format outputFormat, out, errOut io.Writer, quiet bool) ([]byte, error) {
	decoder := json.NewDecoder(resp.Body)
	encoder := json.NewEncoder(out)
	var renderer *deployEventRenderer
	if format != formatNDJSON && !quiet {
		renderer = newDeployEventRenderer(errOut)
	}
	for {
		var event deployevent.Event
		if err := decoder.Decode(&event); err != nil {
			if renderer != nil {
				renderer.stop()
			}
			if err == io.EOF {
				return nil, &ExitCodeError{Code: 1, Kind: KindInternal, Err: &hintedMsgError{
					msg:  "deployment event stream ended without a result",
					hint: "retry the deploy; if it repeats, compare the CLI and server versions with `shinyhub doctor`",
				}}
			}
			return nil, fmt.Errorf("read deployment progress: %w", err)
		}
		if format == formatNDJSON {
			if err := encoder.Encode(event); err != nil {
				return nil, err
			}
		}
		if renderer != nil {
			renderer.handle(event)
		}
		switch event.Type {
		case deployevent.TypeResult:
			if renderer != nil {
				renderer.stop()
			}
			if len(event.Result) == 0 {
				return nil, fmt.Errorf("deployment result event contained no result")
			}
			return event.Result, nil
		case deployevent.TypeError:
			if renderer != nil {
				renderer.stop()
			}
			status := event.StatusCode
			if status == 0 {
				status = http.StatusInternalServerError
			}
			phase := event.Phase
			if phase == "" {
				phase = "deployment"
			}
			err := &httpStatusError{Status: status, msg: fmt.Sprintf("deploy failed during %s: %s", phase, event.Message)}
			if format == formatNDJSON {
				_, code := statusKind(status)
				return nil, &ExitCodeError{Code: code, Err: err, Reported: true}
			}
			return nil, err
		}
	}
}

func writeDeployEvent(w io.Writer, event deployevent.Event) error {
	return json.NewEncoder(w).Encode(event)
}

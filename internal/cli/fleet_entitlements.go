package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"

	"github.com/BurntSushi/toml"
	"github.com/rvben/shinyhub/internal/deploy"
	policy "github.com/rvben/shinyhub/internal/entitlementpolicy"
	"github.com/rvben/shinyhub/internal/fleet"
)

// Resolve policy before environment materialization so the runtime manifest
// cannot ship authorization policy in a bundle that may later be rolled back.
func resolveFleetEntitlements(spec bundleBuildSpec, override policy.Policy) (bundleBuildSpec, policy.Policy, error) {
	data, err := fleetSourceManifestData(spec.Dir)
	if err != nil || data == nil {
		return spec, override, err
	}
	manifest, err := deploy.ParseSourceManifest(data)
	if err != nil {
		return spec, nil, err
	}
	if manifest.Entitlements == nil {
		return spec, override, nil
	}
	var root map[string]any
	if _, err := toml.Decode(string(data), &root); err != nil {
		return spec, nil, err
	}
	delete(root, "entitlements")
	if len(root) == 0 {
		root["sparse"] = true
	}
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(root); err != nil {
		return spec, nil, err
	}
	spec.Manifest = out.Bytes()
	if override != nil {
		return spec, override, nil
	}
	return spec, manifest.Entitlements, nil
}

const omittedEntitlementPolicyWarning = "entitlements policy omitted; existing managed entitlements are retained (use an empty [entitlements] table to clear them)"

type fleetEntitlementRequest struct {
	Owner   string        `json:"owner"`
	Desired policy.Policy `json:"desired"`
	Token   string        `json:"token,omitempty"`
}

func previewFleetEntitlements(cfg *cliConfig, slug, owner string, desired policy.Policy) (policy.Preview, error) {
	body, err := json.Marshal(fleetEntitlementRequest{Owner: owner, Desired: desired})
	if err != nil {
		return policy.Preview{}, err
	}
	res, err := doAPI(cfg, apiCall{Method: http.MethodPost, Path: "/api/apps/" + url.PathEscape(slug) + "/entitlement-policy/preview", MaxResponseBytes: 16 << 20, Body: body, Op: "preview entitlement policy for " + slug})
	if err != nil {
		return policy.Preview{}, err
	}
	var preview policy.Preview
	if err := json.Unmarshal(res.Body, &preview); err != nil {
		return policy.Preview{}, &protocolError{op: "decode entitlement preview", err: err}
	}
	if preview.AppID <= 0 || (desired != nil && (preview.Token == "" || preview.Changes == nil)) {
		return policy.Preview{}, &protocolError{op: "decode entitlement preview", err: fmt.Errorf("missing app identity or policy preview")}
	}
	return preview, nil
}

func applyFleetEntitlements(cfg *cliConfig, slug, owner, runID string, desired policy.Policy, preview policy.Preview, createdID int64) error {
	if createdID != 0 {
		current, err := previewFleetEntitlements(cfg, slug, owner, desired)
		if err != nil {
			return err
		}
		if current.AppID != createdID || !reflect.DeepEqual(current.Plan, preview.Plan) {
			return &conflictError{slug: slug, msg: "app identity or entitlements changed after creation; run fleet plan again"}
		}
		preview = current
	}
	body, err := json.Marshal(fleetEntitlementRequest{Owner: owner, Desired: desired, Token: preview.Token})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, cfg.Host+"/api/apps/"+url.PathEscape(slug)+"/entitlement-policy", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(cfg.Token))
	decorateFleetRequest(req, runID)
	res, err := httpClient.Do(req)
	if err != nil {
		return &entitlementOutcomeUnknown{err}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNoContent {
		return nil
	}
	if res.StatusCode < 400 || res.StatusCode == http.StatusBadGateway ||
		res.StatusCode == http.StatusServiceUnavailable || res.StatusCode == http.StatusGatewayTimeout {
		return &entitlementOutcomeUnknown{fmt.Errorf("unexpected HTTP response %s", res.Status)}
	}
	response, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode == http.StatusConflict {
		return &conflictError{slug: slug, msg: serverErrorMessage(response, res.Status)}
	}
	return httpError(cfg.Token, "apply entitlement policy for "+slug, res, response)
}

type entitlementOutcomeUnknown struct{ cause error }

func (e *entitlementOutcomeUnknown) Error() string {
	return "entitlement apply outcome unknown: " + e.cause.Error()
}

func (e *entitlementOutcomeUnknown) Unwrap() error { return e.cause }

func isEntitlementOutcomeUnknown(err error) bool {
	var unknown *entitlementOutcomeUnknown
	return errors.As(err, &unknown)
}

func fleetEntitlementPlan(d fleet.AppDiff) *policy.Plan {
	if d.Entitlements == nil {
		return nil
	}
	return &d.Entitlements.Plan
}

func fleetReportedAction(d fleet.AppDiff) string {
	if d.Action == fleet.ActionUnchanged && d.Entitlements != nil && len(d.Entitlements.Changes) != 0 {
		return string(fleet.ActionUpdateEntitlements)
	}
	return string(d.Action)
}

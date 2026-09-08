package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Keep the HTTP status available to automation while identifying refusals that
// cannot be diagnosed from an earlier app process's logs.
type diskQuotaError struct{ err *httpStatusError }

func (e *diskQuotaError) Error() string { return e.err.Error() }
func (e *diskQuotaError) Unwrap() error { return e.err }
func isDiskQuotaError(err error) bool {
	var quota *diskQuotaError
	return errors.As(err, &quota)
}

func parseDiskQuotaError(op string, status int, body []byte) error {
	if status != http.StatusRequestEntityTooLarge {
		return nil
	}
	var detail struct {
		Error string `json:"error"`
		Used  *int64 `json:"used_mb"`
		Limit *int64 `json:"quota_mb"`
	}
	if json.Unmarshal(body, &detail) != nil || detail.Error != "app disk quota exceeded" {
		return nil
	}
	message := op + " blocked: disk quota exceeded"
	if detail.Used != nil && detail.Limit != nil && *detail.Used >= 0 && *detail.Limit > 0 {
		message += fmt.Sprintf("; using %s of %s", quotaSize(*detail.Used), quotaSize(*detail.Limit))
	}
	message += ". Usage includes retained deployments, runtime environments, and persistent app data. Ask the server operator to increase storage.app_quota_mb with enough room for retained versions and the next deployment, then retry."
	return &diskQuotaError{err: &httpStatusError{Status: status, msg: message}}
}

func quotaSize(mib int64) string {
	if mib < 1024 {
		return fmt.Sprintf("%d MiB", mib)
	}
	if mib%1024 == 0 {
		return fmt.Sprintf("%d GiB", mib/1024)
	}
	return fmt.Sprintf("%.2f GiB", float64(mib)/1024)
}

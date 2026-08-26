package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/slug"
)

type serviceAccountResponse struct {
	ID        int64     `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	Username  string    `json:"username"`
	ManagedBy string    `json:"managed_by"`
	CreatedAt time.Time `json:"created_at"`
}

func serviceAccountView(u *db.User) serviceAccountResponse {
	name := u.DisplayName
	if name == "" {
		name = u.ServiceAccountKey
	}
	return serviceAccountResponse{ID: u.ID, Key: u.ServiceAccountKey, Name: name,
		Username: u.Username, ManagedBy: u.ManagedBy, CreatedAt: u.CreatedAt}
}

func requireHumanAdmin(w http.ResponseWriter, r *http.Request) (*auth.ContextUser, bool) {
	u, ok := requireAdmin(w, r)
	if !ok {
		return nil, false
	}
	if u.IsServiceAccount() {
		writeError(w, http.StatusForbidden, "service account credentials can only be managed by a human administrator")
		return nil, false
	}
	return u, true
}

func (s *Server) deploymentServiceAccount(w http.ResponseWriter, r *http.Request) (*db.User, bool) {
	key := chi.URLParam(r, "key")
	account, err := s.store.GetServiceAccount(key)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "service account not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	return account, true
}

func (s *Server) handleListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHumanAdmin(w, r); !ok {
		return
	}
	accounts, err := s.store.ListServiceAccounts()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	items := make([]serviceAccountResponse, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, serviceAccountView(account))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetServiceAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHumanAdmin(w, r); !ok {
		return
	}
	account, ok := s.deploymentServiceAccount(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service_account": serviceAccountView(account)})
}

func (s *Server) handleListServiceCredentials(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHumanAdmin(w, r); !ok {
		return
	}
	account, ok := s.deploymentServiceAccount(w, r)
	if !ok {
		return
	}
	credentials, err := s.store.ListServiceCredentials(account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": credentials})
}

type createServiceCredentialRequest struct {
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	Apps          []string `json:"apps"`
	Unrestricted  bool     `json:"unrestricted"`
	ExpiresInDays int      `json:"expires_in_days"`
}

type createServiceCredentialResponse struct {
	db.APIKeyInfo
	Token   string `json:"token"`
	Warning string `json:"warning,omitempty"`
}

func (s *Server) handleCreateServiceCredential(w http.ResponseWriter, r *http.Request) {
	admin, ok := requireHumanAdmin(w, r)
	if !ok {
		return
	}
	account, ok := s.deploymentServiceAccount(w, r)
	if !ok {
		return
	}
	var req createServiceCredentialRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 64 {
		writeError(w, http.StatusBadRequest, "name must be between 1 and 64 characters")
		return
	}
	if strings.EqualFold(req.Name, db.DeployTokenCredentialName) {
		writeError(w, http.StatusConflict, "credential name is reserved for server configuration")
		return
	}
	if req.Role == "" {
		req.Role = "developer"
	}
	if !auth.IsValidGlobalRole(req.Role) {
		writeError(w, http.StatusBadRequest, "role must be viewer, developer, operator, or admin")
		return
	}
	if req.Unrestricted == (len(req.Apps) > 0) {
		writeError(w, http.StatusBadRequest, "choose either a non-empty apps allowlist or unrestricted access")
		return
	}
	seen := make(map[string]struct{}, len(req.Apps))
	for _, app := range req.Apps {
		if !slug.Valid(app) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid app slug %q", app))
			return
		}
		if _, duplicate := seen[app]; duplicate {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("duplicate app slug %q", app))
			return
		}
		seen[app] = struct{}{}
	}
	sort.Strings(req.Apps)
	if req.ExpiresInDays == 0 {
		req.ExpiresInDays = defaultTokenExpiryDays
	}
	if req.ExpiresInDays < 1 || req.ExpiresInDays > maxTokenExpiryDays {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("expires_in_days must be between 1 and %d", maxTokenExpiryDays))
		return
	}
	if exists, err := s.store.APIKeyNameExists(account.ID, req.Name); err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	} else if exists {
		writeError(w, http.StatusConflict, "credential name already in use")
		return
	}
	raw, hash, err := generateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	expiresAt := time.Now().UTC().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
	id, createdAt, err := s.store.CreateAPIKey(db.CreateAPIKeyParams{
		UserID: account.ID, KeyHash: hash, Name: req.Name, ExpiresAt: &expiresAt,
		CredentialType: "service", CredentialRole: req.Role, AppScope: req.Apps,
		Unrestricted: req.Unrestricted, CreatedByUserID: &admin.ID,
	})
	if err != nil {
		if errors.Is(err, db.ErrReservedCredentialName) {
			writeError(w, http.StatusConflict, "credential name is reserved for server configuration")
			return
		}
		if errors.Is(err, db.ErrAPIKeyNameExists) {
			writeError(w, http.StatusConflict, "credential name already in use")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &admin.ID, Action: "create_service_credential",
		ResourceType: "service_account", ResourceID: account.ServiceAccountKey,
		Detail:    fmt.Sprintf("credential=%s role=%s expires_in_days=%d", req.Name, req.Role, req.ExpiresInDays),
		IPAddress: s.ClientIP(r)})
	credential := db.APIKeyInfo{ID: id, Name: req.Name, CreatedAt: createdAt, ExpiresAt: &expiresAt,
		CredentialType: "service", CredentialRole: req.Role, AppScope: req.Apps,
		Unrestricted: req.Unrestricted, CreatedByUserID: &admin.ID}
	response := createServiceCredentialResponse{APIKeyInfo: credential, Token: raw}
	if req.Role == "admin" {
		response.Warning = "Admin authority over people and server settings is platform-wide. App scope limits app-specific operations; scoped credentials cannot change the project catalog or read the global audit log."
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleDeleteServiceCredential(w http.ResponseWriter, r *http.Request) {
	admin, ok := requireHumanAdmin(w, r)
	if !ok {
		return
	}
	account, ok := s.deploymentServiceAccount(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}
	err = s.store.DeleteServiceCredential(id, account.ID)
	if errors.Is(err, db.ErrManagedCredential) {
		writeError(w, http.StatusConflict, "configuration-managed credentials must be removed from server configuration")
		return
	}
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	s.logAuditEvent(r, db.AuditEventParams{UserID: &admin.ID, Action: "delete_service_credential",
		ResourceType: "service_account", ResourceID: account.ServiceAccountKey, Detail: fmt.Sprintf("credential_id=%d", id),
		IPAddress: s.ClientIP(r)})
	w.WriteHeader(http.StatusNoContent)
}

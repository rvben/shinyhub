package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type envListItem struct {
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Secret    bool   `json:"secret"`
	Set       bool   `json:"set"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s *Server) handleListAppEnv(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	app, _, ok := s.requireViewApp(w, r, slug)
	if !ok {
		return
	}

	vars, err := s.store.ListAppEnvVars(app.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list env")
		return
	}
	out := make([]envListItem, 0, len(vars))
	for _, v := range vars {
		item := envListItem{
			Key:       v.Key,
			Secret:    v.IsSecret,
			Set:       true,
			UpdatedAt: v.UpdatedAt.Unix(),
		}
		if !v.IsSecret {
			item.Value = string(v.Value)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": out})
}

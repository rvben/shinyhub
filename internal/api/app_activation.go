package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleGetAppActivation(w http.ResponseWriter, r *http.Request) {
	app, ok := s.requireManageApp(w, r, chi.URLParam(r, "slug"))
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad activation id")
		return
	}
	a, err := s.store.GetScheduleActivation(id)
	if err != nil || a.AppID == nil || *a.AppID != app.ID {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, a)
}

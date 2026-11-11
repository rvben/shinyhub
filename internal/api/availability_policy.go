package api

import (
	"slices"

	"github.com/rvben/shinyhub/internal/availability"
	"github.com/rvben/shinyhub/internal/db"
)

func (s *Server) availabilityExposure(slug, intent string) availability.Exposure {
	return availability.Resolve(intent, s.cfg.Server.AppAvailabilityOptIn == "allow", s.cfg.Server.AppAvailabilityDefault == "public", slices.Contains(s.cfg.Server.PublicAppAvailability, slug))
}

func (s *Server) decorateAvailability(app *db.App) {
	app.AvailabilityExposure = nil
	if app.CanManage {
		e := s.availabilityExposure(app.Slug, app.Availability)
		app.AvailabilityExposure = &e
	}
}

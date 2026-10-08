package ui

import "github.com/rvben/shinyhub/internal/hubroute"

// ExactUIRoutes and IsUIPath share the route contract with environment links.
func ExactUIRoutes() []string   { return hubroute.ExactUIRoutes() }
func IsUIPath(path string) bool { return hubroute.IsUIPath(path) }

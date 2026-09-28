package router

import "strings"

// Automatic route names. They are not live catalog IDs.
const (
	RouteAuto    = "pea/auto"
	RouteEconomy = "pea/economy"
	RouteLocal   = "pea/local"
	RouteFree    = "pea/free"
)

// Automatic reports whether name is one of the explicit automatic routes.
func Automatic(name string) bool {
	switch strings.TrimSpace(name) {
	case RouteAuto, RouteEconomy, RouteLocal, RouteFree:
		return true
	default:
		return false
	}
}

// AutomaticNames is the full set, used for alias collision checks.
func AutomaticNames() []string {
	return []string{RouteAuto, RouteEconomy, RouteLocal, RouteFree}
}

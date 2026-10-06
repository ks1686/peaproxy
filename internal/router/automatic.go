package router

import (
	"strings"

	"github.com/ks1686/peaproxy/internal/catalog"
)

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

// LocalOnlyRouteAllows reports whether the local route accepts a deployment.
//
// pea/local promises the request stays on this machine. cloudFallback is a
// general escape hatch for automatic routing and must not decide this route:
// honouring it here would let a cloud account answer a request made under a
// name that promises the opposite.
func LocalOnlyRouteAllows(m catalog.Model) bool {
	return m.Tier == catalog.TierLocal
}

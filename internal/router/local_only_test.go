package router

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/catalog"
)

// A local route means the request stays on this machine. cloudFallback is a
// general escape hatch for automatic routing; letting it decide the local route
// meant pea/local could return a cloud account, which is the one thing the route
// name is supposed to promise.
func TestLocalRouteNeverFallsBackToCloud(t *testing.T) {
	cloud := catalog.Model{ID: "m", Tier: catalog.TierPaid}
	local := catalog.Model{ID: "m", Tier: catalog.TierLocal}

	if LocalOnlyRouteAllows(cloud) {
		t.Error("a paid model qualified for the local route")
	}
	if !LocalOnlyRouteAllows(local) {
		t.Error("a local model was refused by the local route")
	}
}

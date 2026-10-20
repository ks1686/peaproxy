package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

// docs/CONFIG.md promises requestEngine and optimization take effect without a
// restart. The spend ceiling lives in optimization and maxInFlight is enforced
// by the admission gate, which only reads what is published to it.
func TestAdoptAppliesOptimizationAndAdmissionLimits(t *testing.T) {
	cfg := config.Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317}
	g, err := New(cfg, "", adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	disk := config.Clone(cfg)
	disk.Optimization.SpendCeilingUSD = 5
	disk.RequestEngine.MaxInFlight = 2
	g.saveMu.Lock()
	_, err = g.adopt(disk)
	g.saveMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Config().SpendCeiling(); got != 5 {
		t.Fatalf("spend ceiling after reload = %v, want 5", got)
	}
	if max := g.admission.MaxInFlight; max != 2 {
		t.Fatalf("admission gate maxInFlight after reload = %d, want 2", max)
	}
}

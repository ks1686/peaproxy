package router

import "testing"

func TestAdaptivePrefersIdleMeasuredAccount(t *testing.T) {
	hot := []string{"busy", "idle"}
	got := AdaptiveOrder(hot, map[string]AccountStat{
		"busy": {InFlight: 4, Known: true},
		"idle": {InFlight: 0, Known: true},
	})
	if len(got) != 2 || got[0] != "idle" {
		t.Fatalf("order %v", got)
	}
}

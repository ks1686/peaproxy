package version

import (
	"runtime/debug"
	"testing"
)

func TestResolvePrefersLdflagsOverModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}
	got := Resolve("1.6.1", info)
	if got != "1.6.1" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveUsesGoInstallModuleVersionWhenLdflagsDev(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.6.0"}}
	got := Resolve("dev", info)
	if got != "v1.6.0" {
		t.Fatalf("go install module version: got %q want v1.6.0", got)
	}
}

func TestResolveEmptyLdflagsUsesModuleVersion(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v1.6.0"}}
	got := Resolve("", info)
	if got != "v1.6.0" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveDevelWithoutVCSStaysDev(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	got := Resolve("dev", info)
	if got != "dev" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveNilBuildInfoStaysDev(t *testing.T) {
	got := Resolve("dev", nil)
	if got != "dev" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveDirtyLocalTreeStaysDev(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := Resolve("dev", info)
	if got != "dev" {
		t.Fatalf("dirty tree: got %q", got)
	}
}

func TestResolveCleanVCSRevisionWhenDevel(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef1234567890"},
			{Key: "vcs.modified", Value: "false"},
		},
	}
	got := Resolve("dev", info)
	if got != "abcdef1" {
		t.Fatalf("clean vcs: got %q want abcdef1", got)
	}
}

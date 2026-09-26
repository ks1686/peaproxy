// Package version reports the binary version for CLI, health, and HTTP.
//
// Precedence:
//  1. GoReleaser ldflags (-X …Version=) when not the "dev" default
//  2. Go module version from runtime/debug (go install …@vX.Y.Z)
//  3. Short VCS revision for a clean local tree
//  4. "dev" for dirty or unknown local builds
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the resolved build version. The compile-time default is "dev";
// GoReleaser overwrites it via ldflags. init() then fills go-install / VCS
// versions when ldflags were left at the default.
var Version = "dev"

func init() {
	Version = Resolve(Version, readBuildInfo())
}

func readBuildInfo() *debug.BuildInfo {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return info
}

// Resolve picks a display version from ldflags and optional build info.
func Resolve(ldflags string, info *debug.BuildInfo) string {
	if stamped := strings.TrimSpace(ldflags); stamped != "" && stamped != "dev" {
		return stamped
	}
	if info == nil {
		return "dev"
	}
	if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
		return v
	}
	revision, modified := vcsMeta(info)
	if revision == "" || modified {
		return "dev"
	}
	return revision
}

func vcsMeta(info *debug.BuildInfo) (revision string, modified bool) {
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
			if len(revision) > 7 {
				revision = revision[:7]
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return revision, modified
}

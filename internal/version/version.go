package version

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// version is set at build time via -ldflags "-X .../internal/version.version=v1.0.0".
// Falls back to "dev" for local builds.
var version = "dev"

// String returns the version string with embedded VCS info when available,
// e.g. "v1.0.0 (commit abc1234d, 2026-04-25T10:00:00Z)" or "v1.0.0 (dirty)".
func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}

	var commit, buildTime string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
			if len(commit) > 8 {
				commit = commit[:8]
			}
		case "vcs.time":
			buildTime = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	var parts []string
	if commit != "" {
		parts = append(parts, "commit "+commit)
	}
	if buildTime != "" {
		parts = append(parts, buildTime)
	}
	if dirty {
		parts = append(parts, "dirty")
	}
	if len(parts) == 0 {
		return version
	}
	return fmt.Sprintf("%s (%s)", version, strings.Join(parts, ", "))
}

// Package version provides the build-time version string for neondoll binaries.
//
// Version is set via ldflags at build time (e.g. -X github.com/Neon-Dolls/neondoll/pkg/version.Version=v1.0.0).
// When not set, it falls back to "dev".
package version

// Version is the semantic version of the build, injected by goreleaser or set by ldflags.
var Version = "dev"

// String is a shorthand for printing the version.
func String() string {
	return Version
}

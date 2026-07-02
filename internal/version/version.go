// Package version holds build metadata injected at link time by GoReleaser
// (-X github.com/RobertYoung/gitlab-reviewer-cli/internal/version.Version=...).
package version

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a single-line human-readable version string.
func String() string {
	return Version + " (commit " + Commit + ", built " + Date + ")"
}

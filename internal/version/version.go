// Package version names the build (REQ-010 fix 16): the release (a tag
// such as v0.1.0, or "dev") and the commit it was built from. The image
// build sets both with -ldflags -X; a plain "go build" leaves the
// defaults.
package version

// Set at build time:
//
//	-ldflags "-X github.com/Abdullah95k/securebrand-microservices-youtube/internal/version.Version=v0.1.0
//	          -X github.com/Abdullah95k/securebrand-microservices-youtube/internal/version.Revision=<commit>"
var (
	Version  = "dev"
	Revision = "unknown"
)

// String is "v0.1.0 (abc1234…)".
func String() string {
	rev := Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return Version + " (" + rev + ")"
}

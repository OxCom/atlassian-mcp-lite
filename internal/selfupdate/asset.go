//go:build selfupdate

package selfupdate

import "runtime"

// assetPrefix is the release asset name prefix for a build that carries this
// package. It is *not* the default binary's prefix.
//
// A selfupdate build must never replace itself with a default build. The
// default build contains no updater, so the swap would be a one-way trip: the
// operator would get one successful update and then a binary that can no longer
// update itself, with no error anywhere to explain why. Encoding the variant in
// the asset name and deriving that name from the build tag — this file exists
// only under the tag — makes the wrong asset unnameable rather than merely
// unlikely.
const assetPrefix = "atlassian-mcp-lite-selfupdate"

// buildGOOS and buildGOARCH name the platform this binary was compiled for.
// They are vars rather than direct uses of the runtime constants so the tests
// can check the Windows suffix rule without needing a Windows machine; nothing
// outside this package can reach them, and a normal build never assigns them.
var (
	buildGOOS   = runtime.GOOS
	buildGOARCH = runtime.GOARCH
)

// assetName returns the release asset this build may replace itself with.
//
// The name is constructed locally from the compiled-in platform, never read
// from the release metadata. Anything taken from the GitHub response is a
// choice made by whoever can edit a release; the one thing the running process
// knows for certain is what it was compiled as.
func assetName() string {
	name := assetPrefix + "_" + buildGOOS + "_" + buildGOARCH
	if buildGOOS == "windows" {
		name += ".exe"
	}
	return name
}

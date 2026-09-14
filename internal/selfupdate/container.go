//go:build selfupdate

package selfupdate

import "os"

// Replacing the binary inside a container is refused, and the refusal is not a
// safety nicety: it is the difference between an update and a lie.
//
// A container's filesystem is discarded on the next redeploy. An in-container
// swap therefore produces a server that reports the new version until something
// restarts the container, at which point it silently reverts to the image's
// binary. A version number that changes back on its own is worse than no
// update at all, because the operator has no reason to look. The image is the
// unit of deployment there, so the correct action is to pull a new one.

// containerMarkerFiles are the filesystem markers a container runtime leaves
// behind. Vars rather than constants so the tests can point them at files they
// create; production never assigns them.
var containerMarkerFiles = []string{
	"/.dockerenv",        // Docker, every version
	"/run/.containerenv", // Podman, and CRI-O in some configurations
}

// containerEnvVars are the environment variables that name a container without
// a filesystem marker. `container` is what systemd-nspawn and Podman set;
// KUBERNETES_SERVICE_HOST is injected into every pod that can reach the API
// server, which covers the distroless and scratch images this project ships,
// where no marker file exists at all.
var containerEnvVars = []string{"container", "KUBERNETES_SERVICE_HOST"}

// lookupEnv is os.LookupEnv, replaced by the tests. LookupEnv rather than
// Getenv because an empty-but-set `container=` still means a container.
var lookupEnv = os.LookupEnv

// statPath is os.Stat, replaced by the tests. Stat and not Open: this asks
// whether a path exists, and opening a file is a class of operation the rest of
// this repository does not do.
var statPath = os.Stat

// inContainer reports whether this process appears to be running inside a
// container. It is deliberately eager — any one marker is enough — because a
// false positive costs the operator a `docker pull` they were going to do
// anyway, while a false negative costs them a version that reverts silently.
func inContainer() bool {
	for _, path := range containerMarkerFiles {
		if _, err := statPath(path); err == nil {
			return true
		}
	}
	for _, name := range containerEnvVars {
		if _, ok := lookupEnv(name); ok {
			return true
		}
	}
	return false
}

// containerRefusal is the message the tool returns in that case. It names the
// action that does work, because a refusal that does not is a dead end, and it
// names no path: the result is read by a model.
const containerRefusal = "self_update: this server is running inside a container, where replacing the binary would be undone by the next redeploy; update the image instead (docker pull) and restart the container"

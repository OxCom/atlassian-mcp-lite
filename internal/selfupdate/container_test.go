//go:build selfupdate

package selfupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// noContainer makes the detector report a plain host, so a test that is not
// about container detection is not at the mercy of the machine it runs on — a
// developer's laptop and a CI runner give different answers, and one of them is
// a container.
func noContainer(t *testing.T) {
	t.Helper()
	prevStat, prevEnv := statPath, lookupEnv
	statPath = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	lookupEnv = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { statPath, lookupEnv = prevStat, prevEnv })
}

// forceContainerMarker makes exactly one marker fire, so each test names the
// single signal it is about.
func forceContainerMarker(t *testing.T, marker string) {
	t.Helper()
	noContainer(t)
	prevStat, prevEnv := statPath, lookupEnv
	statPath = func(path string) (os.FileInfo, error) {
		if path == marker {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
	lookupEnv = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { statPath, lookupEnv = prevStat, prevEnv })
}

func TestInContainerReportsAPlainHostAsNotAContainer(t *testing.T) {
	noContainer(t)
	if inContainer() {
		t.Error("a host with no marker was reported as a container")
	}
}

func TestEachContainerMarkerIsEnoughOnItsOwn(t *testing.T) {
	for _, marker := range containerMarkerFiles {
		t.Run(marker, func(t *testing.T) {
			forceContainerMarker(t, marker)
			if !inContainer() {
				t.Errorf("%s did not signal a container", marker)
			}
		})
	}

	for _, name := range containerEnvVars {
		t.Run(name, func(t *testing.T) {
			noContainer(t)
			prev := lookupEnv
			// An empty-but-set value still means a container, which is why the
			// detector uses LookupEnv rather than Getenv: systemd-nspawn sets a
			// bare `container=`.
			lookupEnv = func(want string) (string, bool) { return "", want == name }
			t.Cleanup(func() { lookupEnv = prev })
			if !inContainer() {
				t.Errorf("%s did not signal a container", name)
			}
		})
	}
}

// TestContainerRefusalChangesNothingOnDisk drives the whole handler, because
// the property is about ordering: the container check has to come before
// anything is downloaded and before anything is written, or the refusal is
// issued over a directory that has already been touched.
func TestContainerRefusalChangesNothingOnDisk(t *testing.T) {
	useTestKey(t)
	forceContainerMarker(t, containerMarkerFiles[0])

	// Any request at all is a failure: the refusal must come before the
	// network, not after a download the operator cannot use.
	unreachable(t)

	old := []byte("the old binary")
	exe := fakeExecutable(t, old)
	dir := filepath.Dir(exe)
	before := listDir(t, dir)

	_, err := NewWith(quietLogger()).Tools()[0].Handle(t.Context(), nil)
	if err == nil {
		t.Fatal("the update ran inside a container")
	}
	// The refusal names the action that does work. A refusal that does not is a
	// dead end for the operator reading it.
	if !containsAll(err.Error(), "container", "docker pull") {
		t.Errorf("err = %v, want it to name docker pull", err)
	}

	if after := listDir(t, dir); after != before {
		t.Errorf("the directory changed: %q -> %q", before, after)
	}
	got, readErr := os.ReadFile(exe) //nolint:gosec // a path this test created
	if readErr != nil {
		t.Fatalf("read executable: %v", readErr)
	}
	if !bytes.Equal(got, old) {
		t.Errorf("executable = %q, want it unchanged", got)
	}
}

// listDir renders a directory as a stable string for comparison.
func listDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	out := ""
	for _, e := range entries {
		info, statErr := e.Info()
		if statErr != nil {
			t.Fatalf("stat %s: %v", e.Name(), statErr)
		}
		out += e.Name() + ":" + info.Mode().String() + ";"
	}
	return out
}

//go:build selfupdate

package main

import (
	"github.com/OxCom/atlassian-mcp-lite/internal/selfupdate"
)

// This file is the tagged half of the surface guard. It exists so that the two
// schema tests in surface_test.go — the one that refuses any property naming a
// local path or an outbound destination, and the one that requires every input
// schema to be closed — actually walk the tool a selfupdate build advertises.
//
// Without it the tagged build would ship one tool that no guard had ever
// looked at, which would turn the build tag from a way of removing a capability
// into a way of removing its checks.
func init() {
	extraSurfaceModules = append(extraSurfaceModules, selfupdate.New())
}

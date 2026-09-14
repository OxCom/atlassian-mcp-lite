//go:build !selfupdate

package main

import "github.com/OxCom/atlassian-mcp-lite/internal/core"

// This file is the default build's half of the optional-module hook, and its
// whole job is to be empty.
//
// run() calls registerExtraDeclarations and registerExtraModules in the two
// registration passes. In a default build they do nothing, so the binary
// contains no updater: no self_update in tools/list, no code that writes a
// file, and no code that makes an outbound request to anything but the
// configured Atlassian host. The alternative — a runtime `if` on a setting —
// would leave that code compiled into every binary and reachable through any
// bug in the gating. Absence is the stronger property, and a build tag is the
// only way to get it.
//
// The two functions take their arguments and ignore them so that the tagged
// file next door can be a drop-in replacement with identical signatures; a
// mismatch is then a compile error in the tagged build rather than a silently
// unregistered module.

// registerExtraDeclarations adds no module in a default build.
func registerExtraDeclarations(*core.Registry) {}

// registerExtraModules adds no module in a default build.
func registerExtraModules(*core.Registry, *core.Logger) {}

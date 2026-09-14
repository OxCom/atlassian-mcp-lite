//go:build selfupdate

package main

import (
	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/selfupdate"
)

// This file is the selfupdate build's half of the optional-module hook. It is
// the only place in cmd/ that names internal/selfupdate, and it exists under
// the tag, so a default build does not even import the package.
//
// Both passes register the module, and they must stay in step. The first pass
// exists only to tell core.Load which domains there are. ATLAS_SELFUPDATE is
// global, so Load reads it whether or not this module is registered, but the
// domain must still be present: registering only in the second pass would leave
// "selfupdate" absent from cfg.Domains, so Registry.Enabled would find no
// capabilities for it and drop the tool with no explanation anywhere.
// Registering only in the first would advertise a tool whose handler has no
// logger.

// registerExtraDeclarations adds the declaration-only updater module, so its
// domain name reaches core.Load.
func registerExtraDeclarations(reg *core.Registry) {
	reg.Register(selfupdate.New())
}

// registerExtraModules adds the functional updater module. It receives the
// logger rather than the config and client: it talks to GitHub with its own
// constrained client and must never hold the Atlassian credential.
func registerExtraModules(reg *core.Registry, log *core.Logger) {
	reg.Register(selfupdate.NewWith(log))
}

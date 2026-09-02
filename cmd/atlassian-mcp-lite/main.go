// Command atlassian-mcp-lite is a minimal MCP server for Jira and Confluence.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/OxCom/atlassian-mcp-lite/internal/confluence"
	"github.com/OxCom/atlassian-mcp-lite/internal/core"
	"github.com/OxCom/atlassian-mcp-lite/internal/jira"
)

// main does nothing but translate an error into an exit status. Everything else
// lives in run, so that deferred work — releasing the signal handler — actually
// happens: os.Exit does not run deferred functions, and a defer sitting above
// one is a lie the compiler will not catch.
func main() {
	if err := run(); err != nil {
		// Logs go to stderr: stdout carries the MCP protocol stream. This one
		// is written directly because a failure here can predate the logger.
		fmt.Fprintf(os.Stderr, "atlassian-mcp-lite: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// A boot logger exists before configuration does, so a configuration error
	// has somewhere to go. It holds no secrets because none are known yet — and
	// core.Load's errors never quote a credential.
	bootLog := core.NewLogger(os.Getenv("ATLAS_LOG"), os.Stderr)

	reg := &core.Registry{}
	// Adding a module is one line here plus its package. Capability env vars
	// are derived from the domain name, so core needs no change.
	reg.Register(jira.New())
	reg.Register(confluence.New())

	// ATLAS_ENV_FILE names the private config file. It is refused unless only
	// its owner can read it: the file holds an API token that carries the full
	// authority of its account. Process environment still wins over the file,
	// so a container can override single values without editing it.
	getenv := os.Getenv
	if path := os.Getenv(core.EnvFileVar); path != "" {
		var err error
		if getenv, err = core.LoadEnvFile(path, os.LookupEnv); err != nil {
			return fmt.Errorf("configuration: %w", err)
		}
		bootLog.Debugf("configuration file %s loaded", path)
	}

	cfg, err := core.Load(getenv, reg.Domains())
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	bootLog.Debugf("configuration loaded for %v", reg.Domains())

	// Both the raw token and the Base64 Basic credential are given to the
	// logger, per core.BasicCredential's contract: an upstream error body can
	// echo back either the token or the whole Authorization header value, and
	// the encoded form is just as reusable as the raw one. core.NewServer
	// redacts tool error results through the same logger, so neither reaches
	// the MCP client either.
	log := core.NewLogger(cfg.LogLevel, os.Stderr, cfg.Token, core.BasicCredential(cfg.Email, cfg.Token))
	client := core.NewClient(cfg, log)

	// Second pass: the same modules, now wired to a client. The first pass
	// existed only to learn the domain names that Load needed.
	reg = &core.Registry{}
	reg.Register(jira.NewWith(cfg, client))
	reg.Register(confluence.NewWith(cfg, client))

	srv, n, err := core.NewServer(cfg, reg, log)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	if n == 0 {
		return errors.New("no tools enabled: every ATLAS_<DOMAIN>_READ is off and no WRITE or DESTRUCTIVE capability is on; enable at least one")
	}
	log.Debugf("serving %d tools over stdio", n)

	// As PID 1 in a scratch container, ignoring SIGTERM means docker stop
	// hangs until the kill timeout on every shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A cancelled context is how a clean shutdown arrives, not a failure.
	if err := core.Serve(ctx, srv); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

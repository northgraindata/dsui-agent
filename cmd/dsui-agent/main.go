// Command dsui-agent connects a private network to a DSUI instance.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/northgraindata/dsui-agent/internal/config"
	"github.com/northgraindata/dsui-agent/internal/tunnel"
)

// Set by GoReleaser: -X main.version=... -X main.commit=...
var (
	version = "dev"
	commit  = "none"
)

const usage = `dsui-agent connects data sources in a private network to a DSUI instance.
It only makes outbound connections and forwards traffic to targets you allow.

Usage:
  dsui-agent [run] [flags]   run the agent (default)
  dsui-agent check [flags]   verify config, DSUI access and targets, then exit
  dsui-agent version         print the version

Flags (also settable as DSUI_* environment variables or in a YAML file):
  --config <file>       YAML config file                          DSUI_CONFIG
  --url <url>           DSUI instance URL                         DSUI_URL
  --token-file <file>   file containing the agent token           DSUI_TOKEN_FILE
  --token <token>       agent token (visible in process list)     DSUI_TOKEN
  --targets <list>      allowlist, comma separated host:port      DSUI_TARGETS
  --name <name>         display name shown in DSUI                DSUI_AGENT_NAME
  --ca-file <file>      extra CA bundle for reaching DSUI         DSUI_CA_FILE
  --log-format <fmt>    text or json                              DSUI_LOG_FORMAT
  --log-level <level>   debug, info, warn, error                  DSUI_LOG_LEVEL

Allowlist entries: host:port, host:*, *.domain:port, 10.0.0.0/16:port.
Proxy for the agent's own egress: HTTPS_PROXY / NO_PROXY.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "run"
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Printf("dsui-agent %s (%s)\n", version, commit)
		return 0
	case "help":
		fmt.Print(usage)
		return 0
	case "run", "check":
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(usage)
			return 0
		}
	}

	cfg, err := config.Load(args, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dsui-agent: %v\n\nRun `dsui-agent help` for usage.\n", err)
		return 2
	}
	log := newLogger(cfg)

	client, err := tunnel.New(cfg, log, version)
	if err != nil {
		log.Error("invalid configuration", "error", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cmd == "check" {
		failed := false
		for _, r := range client.Check(ctx) {
			mark := "ok  "
			if !r.OK {
				mark, failed = "FAIL", true
			}
			fmt.Printf("%s  %s: %s\n", mark, r.Name, r.Detail)
		}
		if failed {
			return 1
		}
		return 0
	}

	log.Info("starting dsui-agent", "version", version, "url", cfg.URL)
	if err := client.Run(ctx); err != nil {
		log.Error("agent stopped", "error", err)
		return 1
	}
	log.Info("stopped")
	return 0
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

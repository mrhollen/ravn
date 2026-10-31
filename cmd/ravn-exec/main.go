// ravn-exec runs a foreground command under an auto-heartbeat claim lease on a ravn
// task (see run.go for the AC2/AC3 lifecycle it enforces). Usage:
//
//	ravn-exec --task-id <id> [--ttl-sec N] [--interval DUR] -- <command> [args...]
//
// Config comes from the same environment as ravn-mcp: RAVN_API_URL (upstream),
// RAVN_TOKEN (bearer) and RAVN_AGENT_NAME (the actor that claims/releases). The
// child's stdin/stdout/stderr are wired straight through — this process is a
// thin shell around the lease lifecycle, never an I/O proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stderr))
}

// cliResolution is everything resolved from argv and environment for one run:
// the wrapped command's lease options plus the upstream API URL and bearer token.
type cliResolution struct {
	apiURL string
	token  string
	opts   Options
}

// parseCLIArgs parses ravn-exec's flags, validates them and resolves the RAVN_*
// env overrides (RAVN_API_URL, RAVN_TOKEN, RAVN_AGENT_NAME). It is split from
// runCLI so flag parsing, validation and env resolution can be tested WITHOUT
// spawning children or touching the network. On bad input it returns exit code 2
// with the usage already written to errW — same messages and codes as before the
// split; no behavior changed.
func parseCLIArgs(args []string, errW io.Writer) (cliResolution, int) {
	fs := flag.NewFlagSet("ravn-exec", flag.ContinueOnError)
	fs.SetOutput(errW)

	var (
		taskID   = fs.String("task-id", "", "the ravn task ULID to lease for this run (required)")
		ttlSec   = fs.Int("ttl-sec", DefaultTTLSec, "lease TTL in seconds taken on a fresh claim (default 900, server max 3600)")
		interval = fs.Duration("interval", 0, "heartbeat interval while the command runs; default is --ttl-sec/3")
	)

	if err := fs.Parse(args); err != nil { // bad flag value / unknown flag: usage already printed to errW
		return cliResolution{}, 2
	}

	command := fs.Args() // everything after the flags (and any explicit "--")
	if *taskID == "" {
		fmt.Fprintln(errW, "usage: ravn-exec --task-id <id> [--ttl-sec N] [--interval DUR] -- <command> [args...]")
		return cliResolution{}, 2
	}
	if len(command) == 0 {
		fmt.Fprintln(errW, "ravn-exec: a command to run after the flags is required")
		return cliResolution{}, 2
	}

	apiURL := envOr("RAVN_API_URL", "http://ravn-server:8080") // same default as ravn-mcp
	token := os.Getenv("RAVN_TOKEN")
	actor := apiclient.Actor{Type: "agent", Name: envOr("RAVN_AGENT_NAME", "agent")}

	return cliResolution{
		apiURL: apiURL,
		token:  token,
		opts: Options{
			TaskID:            *taskID,
			Actor:             actor,
			TTLSec:            *ttlSec,
			HeartbeatInterval: *interval,
			Command:           command,
		},
	}, 0
}

// runCLI is the injectable CLI entry point: argv + a stderr writer in, exit code
// out. It parses and resolves via parseCLIArgs (testable seam), then wires the
// resolved values into Run with the real client and foreground runner.
func runCLI(args []string, errW io.Writer) int {
	res, code := parseCLIArgs(args, errW)
	if code != 0 {
		return code
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return Run(ctx, apiclient.New(res.apiURL, res.token), res.opts, runForeground(res.opts.Command))
}

// runForeground is the production runWrapped body: it execs command with stdin,
// stdout and stderr wired straight through to THIS process (foreground streaming)
// and returns the child's exit code. A non-runnable or failing child never aborts
// ravn-exec — Run() still posts the failure comment and releases the lease; a
// command that cannot even start is signalled with 127 (the shell convention for
// "not found"), distinct from any real child exit code.
func runForeground(command []string) func() int {
	return func() int {
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode() // the child exited non-zero — preserve its code
			}
			fmt.Fprintf(os.Stderr, "ravn exec: failed to run %q: %v\n", command[0], err)
			return 127 // command not found / not executable — a distinct non-zero signal
		}
		return 0
	}
}

// envOr returns the first non-empty of $key / def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ravn-exec wraps a foreground command in an auto-heartbeat claim lease so a
// long, SILENT agent work session (build, test, migration) keeps holding its task
// for the entire run — AC2 — and records a progress comment on failure before it
// ALWAYS releases the lease — AC3. This file holds the injectable core; main.go
// is a thin flag/env/stdio shell around it.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mrhollen/ravn/internal/infrastructure/apiclient"
)

// DefaultTTLSec is the lease length a fresh claim takes when --ttl-sec is omitted.
// It matches the server's default claim TTL so an unconfigured run behaves the
// same as a plain MCP claim_task.
const DefaultTTLSec = 900 // seconds; server max is 3600

// apiLease is the minimal slice of *apiclient.Client that Run depends on. It is
// declared as an interface so tests can inject a recording fake, but it is
// satisfied STRUCTURALLY by the real client — production passes apiclient.New(...)
// with no adapter and without forking a single line of API logic (reused, not
// reimplemented).
type apiLease interface {
	ClaimTask(ctx context.Context, id string, actor apiclient.Actor, ttlSec int) (*apiclient.TaskSummary, error)
	HeartbeatTask(ctx context.Context, id string, actor apiclient.Actor) (*apiclient.TaskSummary, error)
	ReleaseTask(ctx context.Context, id string, actor apiclient.Actor, note string) (*apiclient.TaskSummary, error)
	AddComment(ctx context.Context, id string, req apiclient.CommentRequest) (*apiclient.Comment, error)
}

// Options configures one Run call. Command is the argv of the wrapped foreground
// process (Command[0] is the executable). HeartbeatInterval is optional: when zero
// it defaults to ttlSec/3 — comfortably under the lease TTL — so keep-alives bridge
// a silent agent's work long before the lease could expire.
type Options struct {
	TaskID            string
	Actor             apiclient.Actor
	TTLSec            int
	HeartbeatInterval time.Duration
	Command           []string
}

// Run acquires (or reuses) the task's claim lease, keeps it alive with a background
// heartbeat while runWrapped executes the foreground command, then ALWAYS releases
// the lease. If runWrapped exits non-zero, one progress comment recording the
// command and exit code is posted BEFORE the release so the next agent sees why the
// run died.
//
// The lifecycle, in order:
//  1. Probe our own heartbeat; on a miss (we do not hold the lease yet) take a
//     fresh claim with the requested TTL. If that also fails, return a non-zero
//     code WITHOUT running the command — never run under someone else's lease.
//  2. Start a background ticker that re-sends heartbeats every HeartbeatInterval.
//  3. Run the wrapped foreground command (it streams its own stdio).
//  4. Stop the loop and wait for it to drain, so no heartbeat races the teardown.
//  5. On non-zero exit, post one progress comment with the command + code.
//  6. Always release the lease (the command ran regardless of its exit code).
//
// runWrapped is injected so tests can drive an arbitrary body (a sleep, a fixed
// exit code) while production passes the real os/exec stdio-wrapped process. It
// owns only the lease lifecycle around that body.
func Run(ctx context.Context, api apiLease, opts Options, runWrapped func() int) (code int) {
	// 1. Acquire the lease: reuse ours if the heartbeat confirms we hold it, else
	//    claim a fresh one. On failure do NOT run the command — report and bail.
	if _, herr := api.HeartbeatTask(ctx, opts.TaskID, opts.Actor); herr != nil {
		if _, cerr := api.ClaimTask(ctx, opts.TaskID, opts.Actor, ttl(opts)); cerr != nil {
			fmt.Fprintf(os.Stderr, "ravn exec: cannot claim task %s (heartbeat: %v; claim: %v) — command not run\n",
				opts.TaskID, herr, cerr)
			return 1
		}
	}

	// 2. Background heartbeat loop, guaranteed to stop and drain before teardown.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := resolveInterval(opts)
		if interval <= 0 {
			return // degenerate TTL: nothing sensible to tick on; lease is short anyway
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, hErr := api.HeartbeatTask(ctx, opts.TaskID, opts.Actor); hErr != nil {
					fmt.Fprintf(os.Stderr, "ravn exec: heartbeat for task %s failed: %v\n", opts.TaskID, hErr)
				}
			}
		}
	}()

	// 3. Run the wrapped foreground command (streams its own stdio).
	code = runWrapped()

	// 4. Stop the loop and wait for it to finish so no heartbeat races the release.
	close(stop)
	<-done

	// 5. On failure, record it on the task thread BEFORE releasing.
	if code != 0 {
		body := fmt.Sprintf("ravn exec: %s exited with code %d", displayCommand(opts.Command), code)
		if _, cerr := api.AddComment(ctx, opts.TaskID, apiclient.CommentRequest{
			Actor: opts.Actor, Kind: "progress", Body: body,
		}); cerr != nil {
			fmt.Fprintf(os.Stderr, "ravn exec: failed to post exit-code comment: %v\n", cerr)
		}
	}

	// 6. Always release the lease — the command ran regardless of its exit code.
	if _, rerr := api.ReleaseTask(ctx, opts.TaskID, opts.Actor, ""); rerr != nil {
		fmt.Fprintf(os.Stderr, "ravn exec: failed to release task %s: %v\n", opts.TaskID, rerr)
	}

	return code
}

// ttl returns the lease TTL for a fresh claim, applying the default when the caller
// left it at zero.
func ttl(opts Options) int {
	if opts.TTLSec > 0 {
		return opts.TTLSec
	}
	return DefaultTTLSec
}

// resolveInterval returns the heartbeat tick interval: an explicitly supplied value
// wins; otherwise it defaults to ttlSec/3 (e.g. 900s → 300s), comfortably under the
// lease TTL so silent work is bridged by keep-alives well before expiry. Returns a
// non-positive duration for degenerate sub-3-second TTLs, which Run treats as "no
// background ticks needed".
func resolveInterval(opts Options) time.Duration {
	if opts.HeartbeatInterval > 0 {
		return opts.HeartbeatInterval
	}
	return time.Duration(ttl(opts)/3) * time.Second
}

// displayCommand renders the wrapped argv for human-readable comments and
// diagnostics, echoing exactly what was requested (no secrets are added).
func displayCommand(cmd []string) string {
	if len(cmd) == 0 {
		return "<empty>"
	}
	return strings.Join(cmd, " ")
}

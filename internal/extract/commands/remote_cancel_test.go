package commands

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// These tests defend the rule that a cancelled run stops: a page fetch or a
// model call that ignores the command context keeps the process waiting until
// its own per-call timeout, however long ago the caller gave up. Each runs the
// command with a 30-second --timeout against a server that never answers,
// cancels the context once the request arrives, and requires a context error
// well inside that timeout.

// cancelPatience is how long a cancelled command may take to return. It is a
// small fraction of the 30-second --timeout the tests pass, so a command that
// waits out its per-call timeout fails it.
const cancelPatience = 5 * time.Second

// blockingServer answers no request: each handler waits until the client goes
// away or the test ends. Every arriving request is signalled on the returned
// channel.
func blockingServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	hit := make(chan struct{}, 8)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanups run last-in first-out: release the handlers, then close.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv, hit
}

// runCancelledOnHit runs cmd with a non-nil, cancellable context, cancels it
// when the blocking server sees a request, and returns the command's error and
// how long the run took. The context is set explicitly because cobra defaults
// cmd.Context() to a non-nil value only under Execute.
func runCancelledOnHit(t *testing.T, cmd *cobra.Command, hit <-chan struct{}, args ...string) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-hit:
			cancel()
		case <-ctx.Done():
		}
	}()

	cmd.SetContext(ctx)
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs(args)
	start := time.Now()
	err := cmd.Execute()
	return time.Since(start), err
}

func requireCancelError(t *testing.T, err error, took time.Duration) {
	t.Helper()
	if err == nil {
		t.Fatal("the command succeeded although its context was cancelled mid-request")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Errorf("the error does not name the cancellation: %v", err)
	}
	if took > cancelPatience {
		t.Errorf("the command took %v to stop after cancellation; want under %v", took, cancelPatience)
	}
}

func resetExtractLinksFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		extractLinksURL = ""
		extractLinksContext = ""
		extractLinksTimeout = 30
		extractLinksJSON = false
		extractLinksMinimal = false
		extractLinksFields = ""
	})
}

// Defends against the extract-links page fetch ignoring cancellation and
// holding the run until the --timeout fires.
func TestExtractLinksPageFetchStopsOnCancel(t *testing.T) {
	resetExtractLinksFlags(t)
	page, hit := blockingServer(t)

	took, err := runCancelledOnHit(t, newExtractLinksCmd(), hit,
		"--url", page.URL, "--timeout", "30", "--json")

	requireCancelError(t, err, took)
}

// Defends against the extract-links model call ignoring cancellation: before
// the fix it ran under a timeout built on context.Background().
func TestExtractLinksModelCallStopsOnCancel(t *testing.T) {
	resetExtractLinksFlags(t)
	model, hit := blockingServer(t)
	pointExtractRelevantAt(t, model)
	noRetryDelay(t)

	took, err := runCancelledOnHit(t, newExtractLinksCmd(), hit,
		"--url", linksPage(t), "--context", "getting started", "--timeout", "30", "--json")

	requireCancelError(t, err, took)
	if err != nil && !strings.Contains(err.Error(), "LLM ranking failed") {
		t.Errorf("the error is not from the model call: %v", err)
	}
}

// Defends against the extract-relevant page fetch ignoring cancellation and
// holding the run until the --timeout fires.
func TestExtractRelevantPageFetchStopsOnCancel(t *testing.T) {
	t.Cleanup(func() {
		extractRelevantContext = ""
		extractRelevantConcurrency = 2
		extractRelevantOutput = ""
		extractRelevantPath = "."
		extractRelevantJSON = false
		extractRelevantMinimal = false
		extractRelevantTimeout = 60
	})
	// A model endpoint so the client is configured; the fetch never reaches it.
	model := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(model.Close)
	pointExtractRelevantAt(t, model)
	page, hit := blockingServer(t)

	took, err := runCancelledOnHit(t, newExtractRelevantCmd(), hit,
		"--path", page.URL, "--context", "anything", "--timeout", "30", "--json")

	requireCancelError(t, err, took)
}

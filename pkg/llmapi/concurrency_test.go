package llmapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewBatchProcessorFloorsConcurrencyAtOne(t *testing.T) {
	for _, in := range []int{-5, 0, 1} {
		bp := NewBatchProcessor(nil, in)
		if bp.Concurrency != 1 {
			t.Errorf("NewBatchProcessor(_, %d).Concurrency = %d, want 1", in, bp.Concurrency)
		}
	}
	if bp := NewBatchProcessor(nil, 4); bp.Concurrency != 4 {
		t.Errorf("Concurrency = %d, want 4", bp.Concurrency)
	}
}

// errgroup.SetLimit panics on a limit of 0, so the floor in the constructor is
// load-bearing rather than cosmetic.
func TestProcessItemsSurvivesTheFlooredConcurrency(t *testing.T) {
	bp := NewBatchProcessor(nil, 0)
	got := bp.ProcessItems(context.Background(), []interface{}{"a"},
		func(context.Context, *LLMClient, interface{}) (interface{}, error) {
			return "ok", nil
		})
	if len(got) != 1 || got[0].Output != "ok" {
		t.Errorf("ProcessItems = %+v", got)
	}
}

func TestProcessItemsReturnsResultsInInputOrder(t *testing.T) {
	// Deliberately finishes the later items first. Results are indexed, not
	// appended, so a correct implementation still returns input order.
	items := []interface{}{0, 1, 2, 3, 4, 5, 6, 7}
	bp := NewBatchProcessor(nil, 4)

	got := bp.ProcessItems(context.Background(), items,
		func(_ context.Context, _ *LLMClient, item interface{}) (interface{}, error) {
			n := item.(int)
			time.Sleep(time.Duration(len(items)-n) * time.Millisecond)
			return fmt.Sprintf("out-%d", n), nil
		})

	if len(got) != len(items) {
		t.Fatalf("got %d results, want %d", len(got), len(items))
	}
	for i, r := range got {
		if r.Index != i {
			t.Errorf("results[%d].Index = %d", i, r.Index)
		}
		if r.Input != items[i] {
			t.Errorf("results[%d].Input = %v, want %v", i, r.Input, items[i])
		}
		if r.Output != fmt.Sprintf("out-%d", i) {
			t.Errorf("results[%d].Output = %v", i, r.Output)
		}
	}
}

// One item failing must not cancel the rest. ProcessItems returns nil from
// every goroutine on purpose so errgroup never cancels the shared context.
func TestProcessItemsKeepsGoingAfterAnItemFails(t *testing.T) {
	boom := errors.New("boom")
	items := []interface{}{"a", "b", "c"}
	bp := NewBatchProcessor(nil, 3)

	got := bp.ProcessItems(context.Background(), items,
		func(_ context.Context, _ *LLMClient, item interface{}) (interface{}, error) {
			if item == "b" {
				return nil, boom
			}
			return "ok", nil
		})

	if got[0].Error != nil || got[2].Error != nil {
		t.Errorf("a sibling failure cancelled healthy items: %+v", got)
	}
	if !errors.Is(got[1].Error, boom) {
		t.Errorf("results[1].Error = %v, want boom", got[1].Error)
	}
	if got[0].Output != "ok" || got[2].Output != "ok" {
		t.Errorf("healthy items lost their output: %+v", got)
	}
}

func TestProcessItemsHonoursTheConcurrencyLimit(t *testing.T) {
	const limit = 3
	var inFlight, peak int64

	items := make([]interface{}, 24)
	for i := range items {
		items[i] = i
	}

	bp := NewBatchProcessor(nil, limit)
	bp.ProcessItems(context.Background(), items,
		func(context.Context, *LLMClient, interface{}) (interface{}, error) {
			n := atomic.AddInt64(&inFlight, 1)
			for {
				p := atomic.LoadInt64(&peak)
				if n <= p || atomic.CompareAndSwapInt64(&peak, p, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			return nil, nil
		})

	if peak > limit {
		t.Errorf("ran %d items at once, limit is %d", peak, limit)
	}
	if peak < 2 {
		t.Errorf("peak concurrency was %d; the batch never actually ran in parallel", peak)
	}
}

func TestProcessItemsOnAnEmptySlice(t *testing.T) {
	bp := NewBatchProcessor(nil, 2)
	if got := bp.ProcessItems(context.Background(), nil, nil); len(got) != 0 {
		t.Errorf("ProcessItems(nil) = %+v, want empty", got)
	}
}

func TestProcessStringsSendsEachPromptToTheClient(t *testing.T) {
	var mu sync.Mutex
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeRequestPrompt(r)
		mu.Lock()
		seen = append(seen, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, "reply to "+body)
	}))
	defer srv.Close()

	client := NewLLMClient("k", srv.URL, "m")
	bp := NewBatchProcessor(client, 2)

	got := bp.ProcessStrings(context.Background(), "be terse", []string{"one", "two", "three"})

	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	for i, want := range []string{"one", "two", "three"} {
		if got[i].Error != nil {
			t.Errorf("results[%d].Error = %v", i, got[i].Error)
			continue
		}
		if got[i].Input != want {
			t.Errorf("results[%d].Input = %v, want %q", i, got[i].Input, want)
		}
		if got[i].Output != "reply to "+want {
			t.Errorf("results[%d].Output = %v", i, got[i].Output)
		}
	}

	mu.Lock()
	sort.Strings(seen)
	mu.Unlock()
	if len(seen) != 3 {
		t.Errorf("the server saw %d prompts, want 3", len(seen))
	}
}

func TestProcessStringsSurfacesAPerPromptError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if decodeRequestPrompt(r) == "bad" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"message":"nope"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"fine"}}]}`)
	}))
	defer srv.Close()

	// MaxRetries 0: a 500 is retryable, and the default three retries at a
	// two-second backoff would make this test take fourteen seconds to assert
	// something that is already decided on the first attempt.
	client := NewLLMClient("k", srv.URL, "m")
	client.MaxRetries = 0
	bp := NewBatchProcessor(client, 2)

	got := bp.ProcessStrings(context.Background(), "", []string{"good", "bad"})
	if got[0].Error != nil {
		t.Errorf("the healthy prompt failed: %v", got[0].Error)
	}
	if got[1].Error == nil {
		t.Error("the failing prompt reported no error")
	}
}

// The progress callback runs from inside each worker goroutine, so both the
// completed counter and the callback itself are reached concurrently. Run under
// -race; without a lock this reports a write/write race on `completed` and
// under-counts.
func TestProcessItemsWithProgressCountsEveryCompletionExactlyOnce(t *testing.T) {
	const n = 32
	items := make([]interface{}, n)
	for i := range items {
		items[i] = i
	}

	var mu sync.Mutex
	counts := map[int]int{}
	var maxCompleted, totalSeen int

	bp := NewBatchProcessor(nil, 8)
	got := bp.ProcessItemsWithProgress(context.Background(), items,
		func(_ context.Context, _ *LLMClient, item interface{}) (interface{}, error) {
			return item, nil
		},
		func(completed, total int, result BatchResult) {
			mu.Lock()
			defer mu.Unlock()
			counts[completed]++
			totalSeen++
			if completed > maxCompleted {
				maxCompleted = completed
			}
			if total != n {
				t.Errorf("progress total = %d, want %d", total, n)
			}
		})

	if len(got) != n {
		t.Fatalf("got %d results, want %d", len(got), n)
	}
	if totalSeen != n {
		t.Errorf("the callback fired %d times, want %d", totalSeen, n)
	}
	if maxCompleted != n {
		t.Errorf("the final progress report said %d of %d", maxCompleted, n)
	}
	for i := 1; i <= n; i++ {
		if counts[i] != 1 {
			t.Errorf("progress value %d was reported %d times, want exactly 1", i, counts[i])
		}
	}
}

func TestProcessItemsWithProgressAcceptsANilCallback(t *testing.T) {
	bp := NewBatchProcessor(nil, 2)
	got := bp.ProcessItemsWithProgress(context.Background(), []interface{}{"a", "b"},
		func(_ context.Context, _ *LLMClient, item interface{}) (interface{}, error) {
			return item, nil
		}, nil)
	if len(got) != 2 || got[0].Output != "a" || got[1].Output != "b" {
		t.Errorf("ProcessItemsWithProgress = %+v", got)
	}
}

func TestNewLLMClientFromConfigCarriesTheConfigThrough(t *testing.T) {
	client := NewLLMClientFromConfig(&APIConfig{
		APIKey:  "sk-test",
		BaseURL: "https://example.invalid/v1",
		Model:   "some-model",
	})
	if client.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", client.APIKey)
	}
	if client.BaseURL != "https://example.invalid/v1" {
		t.Errorf("BaseURL = %q", client.BaseURL)
	}
	if client.Model != "some-model" {
		t.Errorf("Model = %q", client.Model)
	}
	// The retry defaults come from NewLLMClient and are what make a transient
	// 429 survivable, so they are part of this constructor's contract.
	if client.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", client.MaxRetries)
	}
	if client.HTTPClient == nil {
		t.Error("HTTPClient is nil; every request would panic")
	}
}

// GetAPIConfig is environment-only. The help text used to promise a
// .planning/.config/openai_api_key file that nothing ever read; this pins that
// the environment is in fact the whole story.
func TestGetAPIConfigReadsOnlyTheEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")

	cfg := GetAPIConfig()
	if err := cfg.Validate(); err == nil {
		t.Error("Validate passed with no API key in the environment")
	}
	if cfg.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want the default", cfg.BaseURL)
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-anthropic")
	if got := GetAPIConfig(); got.APIKey != "sk-anthropic" {
		t.Errorf("ANTHROPIC_API_KEY is not read as a fallback: %q", got.APIKey)
	}
}

// decodeRequestPrompt returns the last user message of an OpenAI-shaped chat
// request, so a fake server can answer per prompt.
func decodeRequestPrompt(r *http.Request) string {
	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			return req.Messages[i].Content
		}
	}
	return ""
}

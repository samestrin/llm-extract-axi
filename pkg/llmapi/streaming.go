package llmapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultStreamIdleTimeout is the no-activity window applied to streaming
// requests when the caller does not specify one.
const DefaultStreamIdleTimeout = 120 * time.Second

// maxStreamLineSize bounds a single SSE line; review responses can carry
// large chunks but a single data line should never approach this.
const maxStreamLineSize = 4 * 1024 * 1024

// streamChunk is the SSE payload shape for chat completion deltas.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// watchdogReader re-arms the idle watchdog on every successful read so
// liveness is byte-level (any received bytes, not just complete lines or
// content tokens). It also stamps lastActivity, which is what lets the watchdog
// tell a genuinely idle stream from one whose Reset had simply not landed yet.
type watchdogReader struct {
	r            io.Reader
	watchdog     *time.Timer
	idle         time.Duration
	lastActivity *atomic.Int64 // unix nanos of the last read that returned bytes
}

func (wr *watchdogReader) Read(p []byte) (int, error) {
	n, err := wr.r.Read(p)
	if n > 0 {
		wr.lastActivity.Store(time.Now().UnixNano())
		wr.watchdog.Reset(wr.idle)
	}
	return n, err
}

// idleRemaining reports how much longer to wait before declaring a stream idle.
//
// A positive result means activity landed after the watchdog was armed, so the
// timer re-arms for the remainder rather than cancelling a live stream. Zero
// means the stream really was quiet for the whole window. Each re-arm covers a
// shrinking remainder, so a stream that goes truly silent is still cancelled.
func idleRemaining(lastActivity time.Time, idle time.Duration, now time.Time) time.Duration {
	if since := now.Sub(lastActivity); since < idle {
		return idle - since
	}
	return 0
}

// CompleteMessagesStream sends a streaming chat completion request and
// assembles the response content from SSE chunks.
//
// idleTimeout bounds the gap between stream activity (any received bytes
// count as liveness, not just content tokens); the caller's ctx governs the
// total budget. idleTimeout <= 0 uses DefaultStreamIdleTimeout.
//
// Retry semantics match doRequestWithRetry with two additions: a timed-out
// or stalled stream is never retried, and no retry happens after partial
// content has been received (re-sending would force the backend to repeat
// the full prefill).
func (c *LLMClient) CompleteMessagesStream(ctx context.Context, messages []Message, idleTimeout time.Duration) (string, error) {
	if idleTimeout <= 0 {
		idleTimeout = DefaultStreamIdleTimeout
	}

	req := ChatRequest{
		Model:       c.Model,
		Messages:    messages,
		Temperature: c.Temperature,
		Stream:      true,
	}

	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(float64(c.RetryDelay) * math.Pow(c.RetryBackoff, float64(attempt-1)))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}

		content, receivedAny, err := c.doStreamRequest(ctx, req, idleTimeout)
		if err == nil {
			return content, nil
		}

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if !isRetryable(apiErr.StatusCode) {
				return "", err
			}
			lastErr = err
			continue
		}

		if isTimeoutErr(err) {
			return "", err
		}

		if receivedAny {
			return "", err
		}

		lastErr = err
	}

	return "", fmt.Errorf("max retries exceeded: %w", lastErr)
}

// doStreamRequest executes a single streaming attempt. receivedAny reports
// whether any response bytes arrived, so the caller can avoid retrying a
// partially-consumed stream.
func (c *LLMClient) doStreamRequest(ctx context.Context, req ChatRequest, idleTimeout time.Duration) (content string, receivedAny bool, err error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", false, fmt.Errorf("failed to marshal request: %w", err)
	}

	// The watchdog cancels the request when the stream goes quiet for
	// idleTimeout; it is re-armed on every received line.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var idleFired atomic.Bool
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	// The watchdog re-arms itself when bytes landed after it was armed: firing
	// in the gap between a read and its Reset would cancel a live stream. The
	// self-reference needs the two-step declaration.
	var watchdog *time.Timer
	watchdog = time.AfterFunc(idleTimeout, func() {
		if remaining := idleRemaining(time.Unix(0, lastActivity.Load()), idleTimeout, time.Now()); remaining > 0 {
			watchdog.Reset(remaining)
			return
		}
		idleFired.Store(true)
		cancel()
	})
	defer watchdog.Stop()

	wrapIdle := func(err error) error {
		if idleFired.Load() {
			return fmt.Errorf("stream idle timeout: no activity for %s: %w", idleTimeout, context.DeadlineExceeded)
		}
		return err
	}

	url := c.BaseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(streamCtx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return "", false, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return "", false, wrapIdle(fmt.Errorf("request failed: %w", err))
	}
	defer func() { _ = resp.Body.Close() }()
	// Headers arriving is activity too, so stamp it before re-arming.
	lastActivity.Store(time.Now().UnixNano())
	watchdog.Reset(idleTimeout)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", false, newAPIError(resp.StatusCode, body)
	}

	// Liveness is byte-level: every received chunk of the body re-arms the
	// watchdog, so a slow but steadily-arriving response is never mislabeled
	// as an idle timeout.
	body := &watchdogReader{r: resp.Body, watchdog: watchdog, idle: idleTimeout, lastActivity: &lastActivity}

	// Some OpenAI-compatible gateways ignore stream:true and reply with a
	// plain JSON completion; fall back to non-streaming parsing for those.
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(body)
		if err != nil {
			return "", true, wrapIdle(fmt.Errorf("failed to read response: %w", err))
		}
		var chatResp ChatResponse
		if err := json.Unmarshal(body, &chatResp); err != nil {
			return "", true, fmt.Errorf("failed to parse response: %w", err)
		}
		if len(chatResp.Choices) == 0 {
			return "", true, errors.New("no choices in response")
		}
		return chatResp.Choices[0].Message.Content, true, nil
	}

	var sb strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLineSize)

	// SSE groups lines into EVENTS: an event's `data:` lines accumulate, are
	// joined with "\n", and a BLANK line dispatches the result. Decoding each
	// line on its own worked only because every provider in use emits
	// single-line events — a spec-legal split payload failed to parse.
	var event []string

	// dispatchEvent decodes one completed event into sb. done reports a [DONE]
	// terminator, which ends the stream.
	dispatchEvent := func() (done bool, err error) {
		if len(event) == 0 {
			return false, nil
		}
		data := strings.Join(event, "\n")
		event = event[:0]
		if data == "[DONE]" {
			return true, nil
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, fmt.Errorf("failed to parse stream chunk: %w", err)
		}
		if len(chunk.Choices) > 0 {
			sb.WriteString(chunk.Choices[0].Delta.Content)
		}
		return false, nil
	}

	// assembled is the [DONE] result. A terminated stream with zero content is a
	// silently-empty review, so it fails loudly.
	assembled := func() (string, bool, error) {
		if sb.Len() == 0 {
			return "", true, errors.New("no content in stream")
		}
		return sb.String(), true, nil
	}

	for scanner.Scan() {
		receivedAny = true

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			done, err := dispatchEvent()
			if err != nil {
				return "", receivedAny, err
			}
			if done {
				return assembled()
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		event = append(event, strings.TrimSpace(data))
	}

	if err := scanner.Err(); err != nil {
		return "", receivedAny, wrapIdle(fmt.Errorf("stream read failed: %w", err))
	}

	// A stream that ends without a trailing blank line still holds one event;
	// dispatch it rather than dropping it.
	done, err := dispatchEvent()
	if err != nil {
		return "", receivedAny, err
	}
	if done {
		return assembled()
	}

	// Stream closed without [DONE]; accept assembled content if any arrived.
	if sb.Len() == 0 {
		return "", receivedAny, errors.New("no content in stream")
	}
	return sb.String(), true, nil
}

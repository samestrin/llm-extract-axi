package commands

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// priceInvalid is JSON that breaks priceSchema: the price is a string.
const priceInvalid = `{"price":"three"}`

// priceValidationError is the validator's own message for priceInvalid, so a
// test can look for it without restating its wording.
func priceValidationError(t *testing.T) string {
	t.Helper()
	var s jsonschema.Schema
	if err := json.Unmarshal([]byte(priceSchema), &s); err != nil {
		t.Fatal(err)
	}
	rs, err := s.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal([]byte(priceInvalid), &v); err != nil {
		t.Fatal(err)
	}
	err = rs.Validate(v)
	if err == nil {
		t.Fatal("priceInvalid validates against priceSchema")
	}
	return err.Error()
}

// promptBytes is what bytes_sent counts for one request.
func promptBytes(req schemaRequest) int64 {
	return int64(len(req.message("system")) + len(req.message("user")))
}

// Defends the happy path: an answer that validates the first time is
// returned after one call, with no retry.
func TestSchemaValidFirstTryMakesOneCall(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if n := len(m.sent()); n != 1 {
		t.Fatalf("%d requests, want 1: a valid answer is not retried", n)
	}
	got := schemaRunResult(t, out)
	if got.ModelCalls == nil || *got.ModelCalls != 1 {
		t.Errorf("model_calls = %v, want 1", got.ModelCalls)
	}
	if len(got.ExtractedParts) != 1 || got.ExtractedParts[0] != `{"price":3}` {
		t.Errorf("extracted_parts = %q, want the valid answer", got.ExtractedParts)
	}
	if got.InvalidOutput != "" {
		t.Errorf("invalid_output = %q on a valid answer", got.InvalidOutput)
	}
}

// Defends the one retry: an answer that breaks the schema is asked for again
// once, with the validation errors in the prompt, and the valid second answer
// is what the run returns. Both calls are counted.
func TestSchemaInvalidThenValidRetriesOnceWithTheErrors(t *testing.T) {
	m := newSchemaModel(t, "")
	m.respond = func(req schemaRequest) string {
		if strings.Contains(req.message("user"), relevantSchemaRetryLead) {
			return `{"price":3}`
		}
		return priceInvalid
	}
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) != 2 {
		t.Fatalf("%d requests, want 2 (answer, retry)", len(sent))
	}
	retry := sent[1].message("user")
	if !strings.Contains(retry, priceValidationError(t)) {
		t.Errorf("the retry prompt does not carry the validation error %q:\n%s", priceValidationError(t), retry)
	}
	if !strings.HasPrefix(retry, sent[0].message("user")) {
		t.Errorf("the retry prompt does not repeat the first prompt, content and schema included")
	}
	assertSchemaRequest(t, sent[1], priceSchema)
	got := schemaRunResult(t, out)
	if got.ModelCalls == nil || *got.ModelCalls != 2 {
		t.Errorf("model_calls = %v, want 2: the retry is a model call", got.ModelCalls)
	}
	if want := promptBytes(sent[0]) + promptBytes(sent[1]); got.BytesSent == nil || *got.BytesSent != want {
		t.Errorf("bytes_sent = %v, want %d: both prompts", got.BytesSent, want)
	}
	if len(got.ExtractedParts) != 1 || got.ExtractedParts[0] != `{"price":3}` {
		t.Errorf("extracted_parts = %q, want only the valid retry answer", got.ExtractedParts)
	}
	if got.Error != "" || got.InvalidOutput != "" {
		t.Errorf("error = %q, invalid_output = %q on a run that ended valid", got.Error, got.InvalidOutput)
	}
}

// Defends "never as success": an answer still invalid after the one retry
// fails the run, exit 1, with the errors and the raw answer in invalid_output
// and nothing in extracted_parts. Exactly two calls are made and counted.
func TestSchemaInvalidTwiceFailsWithInvalidOutput(t *testing.T) {
	m := newSchemaModel(t, priceInvalid)
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if n := len(m.sent()); n != 2 {
		t.Fatalf("%d requests, want 2: one retry, no more", n)
	}
	got := schemaRunResult(t, out)
	if got.ModelCalls == nil || *got.ModelCalls != 2 {
		t.Errorf("model_calls = %v, want 2", got.ModelCalls)
	}
	if !strings.Contains(got.Error, priceValidationError(t)) || !strings.Contains(got.Error, "--schema") {
		t.Errorf("error = %q, want it to name --schema and the validation error %q", got.Error, priceValidationError(t))
	}
	if got.InvalidOutput != priceInvalid {
		t.Errorf("invalid_output = %q, want the raw answer %q", got.InvalidOutput, priceInvalid)
	}
	if len(got.ExtractedParts) != 0 || got.Total != nil {
		t.Errorf("extracted_parts = %q, total = %v: an invalid answer was returned as data", got.ExtractedParts, got.Total)
	}
}

// Defends an answer that is not JSON at all, such as prose or NONE: it is
// invalid too, retried with a message saying so, never read as "nothing
// relevant".
func TestSchemaNotJSONAnswerIsRetried(t *testing.T) {
	for _, first := range []string{"The price is 3 dollars.", "NONE", "", `{"price":3} trailing`} {
		t.Run(first, func(t *testing.T) {
			m := newSchemaModel(t, "")
			m.respond = func(req schemaRequest) string {
				if strings.Contains(req.message("user"), relevantSchemaRetryLead) {
					return `{"price":3}`
				}
				return first
			}
			file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

			code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			sent := m.sent()
			if len(sent) != 2 {
				t.Fatalf("%d requests, want 2", len(sent))
			}
			if !strings.Contains(sent[1].message("user"), "not JSON") {
				t.Errorf("the retry prompt does not say the answer was not JSON:\n%s", sent[1].message("user"))
			}
			got := schemaRunResult(t, out)
			if len(got.ExtractedParts) != 1 || got.ExtractedParts[0] != `{"price":3}` {
				t.Errorf("extracted_parts = %q, want the retry's answer", got.ExtractedParts)
			}
		})
	}
}

// Defends the other formats: text and minimal output carry the invalid
// output too, so no format loses what --json carries.
func TestSchemaInvalidTwiceInEveryFormat(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"text", nil, []string{"ERROR:", "INVALID OUTPUT:", priceInvalid}},
		{"minimal", []string{"--min"}, []string{`"io"`, `"mc":2`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newSchemaModel(t, priceInvalid)
			file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")
			args := append([]string{"extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema}, tc.args...)
			code, out, _ := runCLI(t, args...)
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output does not contain %q:\n%s", w, out)
				}
			}
		})
	}
}

// Defends the prompt cap on the retry: a chunk filled to the cap, retried
// with a validation error that is longer than the cap itself, still sends a
// prompt no longer than maxRelevantPromptBytes, uncut, and the retry still
// carries the start of the errors.
func TestSchemaRetryPromptFitsTheCap(t *testing.T) {
	huge := `{"price":"` + strings.Repeat("x", 2*maxRelevantPromptBytes) + `"}`
	m := newSchemaModel(t, "")
	m.respond = func(req schemaRequest) string {
		if strings.Contains(req.message("user"), relevantSchemaRetryLead) {
			return `{"price":3}`
		}
		return huge
	}
	file := writeTemp(t, "big.txt", paragraphDoc(700)) // ~70 KB, several chunks

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	var retries int
	for i, req := range sent {
		user := req.message("user")
		if len(user) > maxRelevantPromptBytes {
			t.Errorf("request %d: %d-byte prompt, over the %d-byte cap", i+1, len(user), maxRelevantPromptBytes)
		}
		if strings.Contains(user, "[Content truncated due to length]") {
			t.Errorf("request %d was cut", i+1)
		}
		if strings.Contains(user, relevantSchemaRetryLead) {
			retries++
			if !strings.Contains(user, "price") {
				t.Errorf("request %d: the retry does not carry the start of the errors:\n%.500s", i+1, user)
			}
		}
	}
	if retries < 2 || retries*2 != len(sent) {
		t.Errorf("%d retries in %d requests, want one retry per chunk", retries, len(sent))
	}
	got := schemaRunResult(t, out)
	if got.Truncated != "" {
		t.Errorf("truncated = %q, want no cut", got.Truncated)
	}
}

// Defends a chunked input: a chunk whose answer stays invalid fails the run
// and is named, its raw answer is in invalid_output, and the other chunks'
// valid answers are kept beside the error as a partial result.
func TestSchemaChunkInvalidTwiceFailsTheRun(t *testing.T) {
	m := newSchemaModel(t, "")
	m.respond = func(req schemaRequest) string {
		if strings.Contains(req.message("user"), "p000000 ") {
			return priceInvalid
		}
		return `{"price":3}`
	}
	file := writeTemp(t, "big.txt", paragraphDoc(700))

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	got := schemaRunResult(t, out)
	if !strings.Contains(got.Error, "chunk 1 of") {
		t.Errorf("error = %q, want it to name chunk 1", got.Error)
	}
	if got.InvalidOutput != priceInvalid {
		t.Errorf("invalid_output = %q, want %q", got.InvalidOutput, priceInvalid)
	}
	for _, p := range got.ExtractedParts {
		if p == priceInvalid {
			t.Errorf("the invalid answer is in extracted_parts: %q", got.ExtractedParts)
		}
	}
	if got.ModelCalls == nil || *got.ModelCalls != int64(len(m.sent())) {
		t.Errorf("model_calls = %v, want %d", got.ModelCalls, len(m.sent()))
	}
}

package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// schemaRequest is one chat request the fake schema model received.
type schemaRequest struct {
	ResponseFormat json.RawMessage `json:"response_format"`
	Messages       []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func (r schemaRequest) message(role string) string {
	for _, m := range r.Messages {
		if m.Role == role {
			return m.Content
		}
	}
	return ""
}

// schemaModel stands in for the model endpoint under --schema. It records
// every request, answers answer (or what respond returns, when set), and
// answers 400 to a request carrying response_format when rejectFormat is
// set, or to every request when rejectAll is.
type schemaModel struct {
	mu           sync.Mutex
	requests     []schemaRequest
	rejectFormat bool
	rejectAll    bool
	answer       string
	respond      func(req schemaRequest) string
}

func newSchemaModel(t *testing.T, answer string) *schemaModel {
	t.Helper()
	m := &schemaModel{answer: answer}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req schemaRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		m.mu.Lock()
		m.requests = append(m.requests, req)
		answer := m.answer
		if m.respond != nil {
			answer = m.respond(req)
		}
		m.mu.Unlock()
		if m.rejectAll || (m.rejectFormat && len(req.ResponseFormat) > 0) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"response_format is not supported"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": answer}}}})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	pointExtractRelevantAt(t, srv)
	noRetryDelay(t)
	return m
}

func (m *schemaModel) sent() []schemaRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]schemaRequest(nil), m.requests...)
}

const priceSchema = `{"type":"object","properties":{"price":{"type":"number"}},"required":["price"]}`

// writeTemp writes content to name in a fresh directory and returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// sameJSON reports whether a and b hold the same JSON value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("not JSON: %v: %s", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("not JSON: %v: %s", err, b)
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

// assertSchemaRequest checks the structured request shape: response_format
// json_schema carrying the schema, and the schema in the user prompt too.
func assertSchemaRequest(t *testing.T, req schemaRequest, schema string) {
	t.Helper()
	var rf struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(req.ResponseFormat, &rf); err != nil {
		t.Fatalf("response_format missing or not an object: %v: %s", err, req.ResponseFormat)
	}
	if rf.Type != "json_schema" || rf.JSONSchema.Name == "" {
		t.Errorf("response_format = %s, want type json_schema with a name", req.ResponseFormat)
	}
	if !sameJSON(t, rf.JSONSchema.Schema, []byte(schema)) {
		t.Errorf("response_format schema = %s, want %s", rf.JSONSchema.Schema, schema)
	}
	assertSchemaInPrompt(t, req, schema)
}

// assertSchemaInPrompt checks the user prompt carries the schema, compact.
func assertSchemaInPrompt(t *testing.T, req schemaRequest, schema string) {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(schema)); err != nil {
		t.Fatal(err)
	}
	if user := req.message("user"); !strings.Contains(user, buf.String()) {
		t.Errorf("the user prompt does not carry the schema %s:\n%s", buf.String(), user)
	}
}

func schemaRunResult(t *testing.T, out string) ExtractRelevantResult {
	t.Helper()
	var got ExtractRelevantResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	return got
}

// Defends the inline form: a --schema value that starts with "{" once
// trimmed is the schema itself, sent as response_format json_schema and in
// the prompt, and the model's JSON answer comes back.
func TestSchemaInlineSendsResponseFormatAndPromptSchema(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", "  \n"+priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) != 1 {
		t.Fatalf("%d requests, want 1", len(sent))
	}
	assertSchemaRequest(t, sent[0], priceSchema)
	got := schemaRunResult(t, out)
	if len(got.ExtractedParts) != 1 || got.ExtractedParts[0] != `{"price":3}` {
		t.Errorf("extracted_parts = %q, want the model's JSON", got.ExtractedParts)
	}
	if got.ModelCalls == nil || *got.ModelCalls != 1 {
		t.Errorf("model_calls = %v, want 1", got.ModelCalls)
	}
}

// Defends the file form: a --schema value that does not start with "{" is
// a path, and the file's schema is what is sent.
func TestSchemaFromFileSendsTheFileSchema(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")
	schemaFile := writeTemp(t, "schema.json", "\n"+priceSchema+"\n")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", schemaFile, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) != 1 {
		t.Fatalf("%d requests, want 1", len(sent))
	}
	assertSchemaRequest(t, sent[0], priceSchema)
}

// Defends the run without --schema: it sends no response_format, so an
// endpoint that does not know it is not sent one.
func TestNoSchemaSendsNoResponseFormat(t *testing.T) {
	m := newSchemaModel(t, "a passage")
	file := writeTemp(t, "page.txt", "content")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "x", "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) != 1 || len(sent[0].ResponseFormat) != 0 {
		t.Fatalf("requests = %+v, want one with no response_format", sent)
	}
	if strings.Contains(sent[0].message("user"), "JSON Schema") {
		t.Errorf("a run without --schema put a schema in the prompt:\n%s", sent[0].message("user"))
	}
}

// Defends the fallback: an endpoint that answers 400 to response_format gets
// the request once more without it, the schema still in the prompt. Only the
// fallback is counted: model_calls 1 and bytes_sent one prompt.
func TestSchema400ResendsOnceWithoutResponseFormat(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	m.rejectFormat = true
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) != 2 {
		t.Fatalf("%d requests, want 2 (rejected, then fallback)", len(sent))
	}
	assertSchemaRequest(t, sent[0], priceSchema)
	if len(sent[1].ResponseFormat) != 0 {
		t.Errorf("the fallback still sent response_format: %s", sent[1].ResponseFormat)
	}
	assertSchemaInPrompt(t, sent[1], priceSchema)
	got := schemaRunResult(t, out)
	if got.ModelCalls == nil || *got.ModelCalls != 1 {
		t.Errorf("model_calls = %v, want 1: the rejected call is not counted", got.ModelCalls)
	}
	want := int64(len(sent[1].message("system")) + len(sent[1].message("user")))
	if got.BytesSent == nil || *got.BytesSent != want {
		t.Errorf("bytes_sent = %v, want %d: one prompt", got.BytesSent, want)
	}
	if len(got.ExtractedParts) != 1 || got.ExtractedParts[0] != `{"price":3}` {
		t.Errorf("extracted_parts = %q, want the fallback's answer", got.ExtractedParts)
	}
}

// Defends "once": a fallback that is refused too is an error, not a loop
// and not a success, and the fallback is the one call counted.
func TestSchema400OnFallbackFailsAfterOneResend(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	m.rejectAll = true
	file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if n := len(m.sent()); n != 2 {
		t.Fatalf("%d requests, want 2", n)
	}
	got := schemaRunResult(t, out)
	if !strings.Contains(got.Error, "400") {
		t.Errorf("error = %q, want the 400", got.Error)
	}
	if got.ModelCalls == nil || *got.ModelCalls != 1 {
		t.Errorf("model_calls = %v, want 1", got.ModelCalls)
	}
}

// Defends a 400 that is not about response_format on a run without
// --schema: there is nothing to fall back from, so it is not resent.
func TestNoSchema400IsNotResent(t *testing.T) {
	m := newSchemaModel(t, "a passage")
	m.rejectAll = true
	file := writeTemp(t, "page.txt", "content")

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "x", "--json")
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	if n := len(m.sent()); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

// Defends the schema check: each bad --schema, and --schema with a
// directory --path, is a usage error (exit 2) refused before any model call,
// with no API key needed to learn it.
func TestSchemaUsageErrors(t *testing.T) {
	dir := t.TempDir()
	file := writeTemp(t, "page.txt", "content")
	withRef := func(ref string) string {
		return `{"type":"object","properties":{"a":{"$ref":"` + ref + `"}}}`
	}
	cases := []struct {
		name, schema, path, want string
	}{
		{"missing schema file", filepath.Join(dir, "nope.json"), file, "nope.json"},
		{"empty value", "", file, "--schema"},
		{"invalid JSON inline", `{"type":`, file, "not valid JSON"},
		{"invalid JSON file", writeTemp(t, "bad.json", `{"type":`), file, "not valid JSON"},
		{"trailing data", `{"type":"object"} {}`, file, "not valid JSON"},
		{"array root", writeTemp(t, "arr.json", `[{"type":"object"}]`), file, "object"},
		{"string root", writeTemp(t, "str.json", `"object"`), file, "object"},
		{"boolean root", writeTemp(t, "bool.json", `true`), file, "object"},
		{"http $ref", withRef("http://example.com/s.json"), file, "$ref"},
		{"https $ref", withRef("https://example.com/s.json#/a"), file, "$ref"},
		{"file scheme $ref", withRef("file:///etc/passwd"), file, "$ref"},
		{"urn scheme $ref", withRef("urn:example:schema"), file, "$ref"},
		{"relative file $ref", withRef("defs.json#/a"), file, "$ref"},
		{"dot relative $ref", withRef("./defs.json"), file, "$ref"},
		{"parent relative $ref", withRef("../defs.json"), file, "$ref"},
		{"$ref deep in $defs", `{"$defs":{"a":{"items":[{"$ref":"https://x.test/y"}]}}}`, file, "$ref"},
		{"non-local $dynamicRef", `{"$dynamicRef":"https://x.test/y"}`, file, "$dynamicRef"},
		{"non-string $ref", `{"$ref":5}`, file, "$ref"},
		// A schema the validator cannot use is refused before a model call
		// whose answer could never be checked.
		{"bad keyword value", `{"type":5}`, file, "2020-12"},
		{"another draft", `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object"}`, file, "2020-12"},
		{"dangling local $ref", `{"type":"object","properties":{"a":{"$ref":"#/$defs/missing"}}}`, file, "2020-12"},
		{"directory path", priceSchema, dir, "directory"},
		{"schema path is a directory", dir, file, "not a regular file"},
		{"schema fills a prompt", `{"description":"` + strings.Repeat("x", maxRelevantPromptBytes) + `"}`, file, "must fit"},
		{"schema file over the read cap", writeTemp(t, "huge.json", `{"description":"`+strings.Repeat("x", maxSchemaFileBytes)+`"}`), file, "over"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noAPIKey(t)
			code, out, _ := runCLI(t, "extract-relevant", "--path", tc.path, "--context", "x", "--schema", tc.schema, "--json")
			if code != 2 {
				t.Fatalf("exit %d, want 2:\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("the error does not name %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, "API key") {
				t.Errorf("the schema was not checked before the API key:\n%s", out)
			}
		})
	}
}

// Defends the other side of the $ref check: schemas whose references are
// all local, or whose "$ref" strings are data or property names, pass. Since
// answers are validated, each model answer here validates against its
// schema, so one request is all the run makes.
func TestSchemaAcceptsLocalRefsAndRefLookingData(t *testing.T) {
	good := []struct{ schema, answer string }{
		{`{"$defs":{"p":{"type":"number"}},"type":"object","properties":{"price":{"$ref":"#/$defs/p"}}}`, `{"price":3}`},
		{`{"type":"object","properties":{"self":{"$ref":"#"}}}`, `{"self":{}}`},
		{`{"$dynamicAnchor":"n","type":"object","properties":{"kids":{"type":"array","items":{"$dynamicRef":"#n"}}}}`, `{"kids":[{}]}`},
		// A property named $ref is a schema, not a reference.
		{`{"type":"object","properties":{"$ref":{"type":"string"}}}`, `{"$ref":"x"}`},
		// Values in enum, const, default and examples are data. (The const
		// equals the enum's one value, so the schema can be met.)
		{`{"enum":[{"$ref":"https://x.test"}],"const":{"$ref":"https://x.test"},"default":{"$ref":"z.json"},"examples":[{"$ref":"file:///a"}]}`, `{"$ref":"https://x.test"}`},
		{`{}`, `{}`},
	}
	for _, tc := range good {
		schema := tc.schema
		t.Run(schema, func(t *testing.T) {
			m := newSchemaModel(t, tc.answer)
			file := writeTemp(t, "page.txt", "content")
			code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "x", "--schema", schema, "--json")
			if code != 0 {
				t.Fatalf("exit %d, want 0:\n%s", code, out)
			}
			if n := len(m.sent()); n != 1 {
				t.Fatalf("%d requests, want 1", n)
			}
		})
	}
}

// Defends --schema on a URL: allowed, and the page is sent with the schema.
func TestSchemaWithURLSendsTheSchema(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("The widget costs 3 dollars."))
	}))
	t.Cleanup(page.Close)

	code, out, _ := runCLI(t, "extract-relevant", "--path", page.URL, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) != 1 {
		t.Fatalf("%d requests, want 1", len(sent))
	}
	assertSchemaRequest(t, sent[0], priceSchema)
}

// Defends a chunked input under --schema: every chunk is sent with the
// schema, and the text reduce call, which would merge JSON answers as prose,
// is not made. The per-chunk answers come back in order and the message says
// they were not merged.
func TestSchemaChunkedInputSkipsTheTextReduce(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	file := writeTemp(t, "big.txt", paragraphDoc(700)) // ~70 KB, several chunks

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) < 2 {
		t.Fatalf("%d requests, want one per chunk (2 or more)", len(sent))
	}
	for i, req := range sent {
		if strings.Contains(req.message("user"), "Extracted parts to merge") {
			t.Fatalf("request %d is a text reduce call", i+1)
		}
		assertSchemaRequest(t, req, priceSchema)
	}
	got := schemaRunResult(t, out)
	if len(got.ExtractedParts) != len(sent) {
		t.Errorf("%d parts, want one per chunk (%d)", len(got.ExtractedParts), len(sent))
	}
	if got.ModelCalls == nil || *got.ModelCalls != int64(len(sent)) {
		t.Errorf("model_calls = %v, want %d", got.ModelCalls, len(sent))
	}
	if !strings.Contains(got.Message, "not merged") {
		t.Errorf("message = %q, want it to say the parts were not merged", got.Message)
	}
}

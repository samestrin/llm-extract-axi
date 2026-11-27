package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

// mergeOf merges parts and fails the test on an error.
func mergeOf(t *testing.T, parts ...string) (string, []schemaConflict) {
	t.Helper()
	merged, conflicts, err := mergeSchemaParts(parts)
	if err != nil {
		t.Fatalf("mergeSchemaParts(%q): %v", parts, err)
	}
	return merged, conflicts
}

// assertMerged checks merged holds the JSON value want.
func assertMerged(t *testing.T, merged, want string) {
	t.Helper()
	if !sameJSON(t, []byte(merged), []byte(want)) {
		t.Errorf("merged = %s, want %s", merged, want)
	}
}

// Defends the array rule: an array property is the chunks' arrays joined in
// chunk order, so no chunk's items are dropped or reordered.
func TestSchemaMergeConcatenatesArraysInOrder(t *testing.T) {
	merged, conflicts := mergeOf(t, `{"names":["a","b"]}`, `{"names":[]}`, `{"names":["c"]}`, `{"names":["a"]}`)
	assertMerged(t, merged, `{"names":["a","b","c","a"]}`)
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none: arrays are joined, not compared", conflicts)
	}
}

// Defends the scalar rule: a scalar takes the first non-null value, so a
// chunk that found nothing (null, or the property left out) never wipes a
// value another chunk found.
func TestSchemaMergeScalarTakesTheFirstNonNull(t *testing.T) {
	merged, conflicts := mergeOf(t, `{"price":null}`, `{}`, `{"price":3}`, `{"price":3}`)
	assertMerged(t, merged, `{"price":3}`)
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none: the later value is the same", conflicts)
	}
}

// Defends the nested-object rule: a nested object is a value as a whole,
// taken from the first chunk that has one, not merged field by field. The
// same object with its keys in another order is the same value.
func TestSchemaMergeNestedObjectIsTakenWhole(t *testing.T) {
	merged, conflicts := mergeOf(t, `{"seller":null}`, `{"seller":{"name":"Acme","city":"Oslo"}}`, `{"seller":{"city":"Oslo","name":"Acme"}}`)
	assertMerged(t, merged, `{"seller":{"name":"Acme","city":"Oslo"}}`)
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v, want none: key order does not make a value differ", conflicts)
	}
}

// Defends the empty cases: a property every chunk left null stays null, and
// one no chunk gave stays out, so the merge invents nothing.
func TestSchemaMergeKeepsNullAndAbsent(t *testing.T) {
	merged, _ := mergeOf(t, `{"price":null}`, `{"price":null,"note":null}`)
	assertMerged(t, merged, `{"price":null,"note":null}`)
	if strings.Contains(merged, "names") {
		t.Errorf("merged = %s, has a property no chunk gave", merged)
	}
}

// Defends the conflict rule: a later non-null value that differs from the
// one kept is listed with its chunk, never dropped silently. Each differing
// value is listed; one equal to the kept value is not.
func TestSchemaMergeListsDifferingValuesAsConflicts(t *testing.T) {
	merged, conflicts := mergeOf(t,
		`{"price":3,"seller":{"name":"Acme"}}`,
		`{"price":4,"seller":{"name":"Acme"}}`,
		`{"price":3,"seller":{"name":"Bolt"}}`,
		`{"price":5}`,
	)
	assertMerged(t, merged, `{"price":3,"seller":{"name":"Acme"}}`)
	got, _ := json.Marshal(conflicts)
	want := `[{"property":"price","kept":3,"kept_chunk":1,"others":[{"chunk":2,"value":4},{"chunk":4,"value":5}]},` +
		`{"property":"seller","kept":{"name":"Acme"},"kept_chunk":1,"others":[{"chunk":3,"value":{"name":"Bolt"}}]}]`
	if !sameJSON(t, got, []byte(want)) {
		t.Errorf("conflicts = %s, want %s", got, want)
	}
}

// Defends a property whose chunks disagree on kind: an array beside a
// scalar cannot be joined, so the first non-null is kept and the other is a
// conflict, not a concatenation of unlike values.
func TestSchemaMergeMixedKindsIsAConflict(t *testing.T) {
	merged, conflicts := mergeOf(t, `{"tag":["a"]}`, `{"tag":"b"}`)
	assertMerged(t, merged, `{"tag":["a"]}`)
	if len(conflicts) != 1 || conflicts[0].Property != "tag" || len(conflicts[0].Others) != 1 || conflicts[0].Others[0].Chunk != 2 {
		t.Errorf("conflicts = %+v, want chunk 2's tag listed", conflicts)
	}
}

// Defends a non-object answer (a schema whose root allows one): the rules
// apply to the whole answer, so an array root is joined and a scalar root
// takes the first non-null, with no property named.
func TestSchemaMergeNonObjectRootUsesTheSameRules(t *testing.T) {
	merged, _ := mergeOf(t, `["a"]`, `["b"]`)
	assertMerged(t, merged, `["a","b"]`)
	merged, conflicts := mergeOf(t, `null`, `"x"`, `"y"`)
	assertMerged(t, merged, `"x"`)
	if len(conflicts) != 1 || conflicts[0].Property != "" || conflicts[0].KeptChunk != 2 {
		t.Errorf("conflicts = %+v, want one for the whole answer, kept from chunk 2", conflicts)
	}
}

// Defends the merge's own input check: a part that is not JSON is an
// error, never merged as if it were empty.
func TestSchemaMergeRefusesAPartThatIsNotJSON(t *testing.T) {
	if _, _, err := mergeSchemaParts([]string{`{"price":3}`, `not json`}); err == nil {
		t.Fatal("mergeSchemaParts accepted a part that is not JSON")
	}
}

// Defends determinism: the same parts merge to the same bytes every time,
// whatever order Go walks the maps in.
func TestSchemaMergeIsDeterministic(t *testing.T) {
	parts := []string{`{"b":1,"a":[1],"c":{"x":1}}`, `{"a":[2],"b":2,"d":null}`}
	first, firstConflicts := mergeOf(t, parts...)
	for range 20 {
		again, conflicts := mergeOf(t, parts...)
		a, _ := json.Marshal(firstConflicts)
		b, _ := json.Marshal(conflicts)
		if again != first || string(a) != string(b) {
			t.Fatalf("merge differs between runs: %s %s vs %s %s", first, a, again, b)
		}
	}
}

// namesSchema allows at most three names, so a merge can break it.
const namesSchema = `{"type":"object","properties":{"names":{"type":"array","items":{"type":"string"},"maxItems":3},"price":{"type":["number","null"]}}}`

// chunkAnswer answers by the chunk the request carries: first for the chunk
// holding the document's first paragraph, rest for every other.
func chunkAnswer(first, rest string) func(req schemaRequest) string {
	return func(req schemaRequest) string {
		if strings.Contains(req.message("user"), "p000000 ") {
			return first
		}
		return rest
	}
}

// Defends the chunked run end to end: the chunks' answers are merged in
// code into one part, no text reduce call is made, and a price that differs
// between chunks is in conflicts[], not hidden, while the run succeeds.
func TestSchemaChunkedRunMergesAndReportsConflicts(t *testing.T) {
	m := newSchemaModel(t, "")
	m.respond = chunkAnswer(`{"names":["a"],"price":3}`, `{"names":["b"],"price":4}`)
	file := writeTemp(t, "big.txt", paragraphDoc(700))

	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "names", "--schema", namesSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	sent := m.sent()
	if len(sent) < 2 {
		t.Fatalf("%d requests, want one per chunk (2 or more)", len(sent))
	}
	for i, req := range sent {
		if strings.Contains(req.message("user"), "Extracted parts to merge") {
			t.Fatalf("request %d is a model reduce call; the --schema merge is in code", i+1)
		}
	}
	got := schemaRunResult(t, out)
	if len(got.ExtractedParts) != 1 {
		t.Fatalf("extracted_parts = %q, want the one merged answer", got.ExtractedParts)
	}
	others := strings.TrimSuffix(strings.Repeat(`"b",`, len(sent)-1), ",")
	assertMerged(t, got.ExtractedParts[0], `{"names":["a",`+others+`],"price":3}`)
	if len(got.Conflicts) != 1 || got.Conflicts[0].Property != "price" || len(got.Conflicts[0].Others) != len(sent)-1 {
		t.Fatalf("conflicts = %+v, want price with every later chunk's 4", got.Conflicts)
	}
	if !strings.Contains(got.Message, "conflict") {
		t.Errorf("message = %q, want it to point at the conflicts", got.Message)
	}
	if got.ModelCalls == nil || *got.ModelCalls != int64(len(sent)) {
		t.Errorf("model_calls = %v, want %d (no reduce call)", got.ModelCalls, len(sent))
	}
}

// Defends the second validation: chunks that each fit maxItems can join to
// an array that breaks it, and that merged answer is a failure, exit 1,
// with the merged answer in invalid_output, never returned as data.
func TestSchemaMergeThatBreaksMaxItemsFails(t *testing.T) {
	m := newSchemaModel(t, "")
	m.respond = chunkAnswer(`{"names":["a","b"]}`, `{"names":["c","d"]}`)
	file := writeTemp(t, "big.txt", paragraphDoc(700))

	for _, format := range [][]string{{"--json"}, {"--json", "--min"}, {}} {
		code, out, _ := runCLI(t, append([]string{"extract-relevant", "--path", file, "--context", "names", "--schema", namesSchema}, format...)...)
		if code != 1 {
			t.Fatalf("%v: exit %d, want 1:\n%s", format, code, out)
		}
		if len(format) == 0 {
			if !strings.Contains(out, "INVALID OUTPUT:") || !strings.Contains(out, `"c","d"`) {
				t.Errorf("text output does not carry the merged invalid output:\n%s", out)
			}
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: not JSON: %v\n%s", format, err, out)
		}
		errKey, ioKey := "error", "invalid_output"
		if len(format) == 2 {
			errKey, ioKey = "e", "io"
		}
		if e, _ := got[errKey].(string); !strings.Contains(e, "merged") {
			t.Errorf("%v: %s = %q, want it to say the merged answer failed", format, errKey, e)
		}
		raw, _ := got[ioKey].(string)
		var invalid struct{ Names []string }
		if err := json.Unmarshal([]byte(raw), &invalid); err != nil || len(invalid.Names) <= 3 || invalid.Names[0] != "a" {
			t.Errorf("%v: %s = %q, want the merged answer with more than 3 names", format, ioKey, raw)
		}
		if _, ok := got["total"]; ok {
			t.Errorf("%v: a failed merge reports a total, as a success would", format)
		}
	}
}

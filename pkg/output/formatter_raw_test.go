package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// sameJSONValue reports whether a and b hold the same JSON value.
func sameJSONValue(t *testing.T, a, b []byte) bool {
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

// Defends raw JSON in minimal output: a json.RawMessage is the JSON value it
// holds, and --min must print it verbatim. The minimal path walked it as a
// []byte and printed {"data":[123,34,...]} — an array of byte numbers — which
// corrupted extract-relevant's --schema data and the conflicts' kept values.
// Nested raw messages (a slice of structs, as conflicts are) are covered too.
func TestMinimalJSONKeepsRawMessageVerbatim(t *testing.T) {
	type conflict struct {
		Property string          `json:"property"`
		Kept     json.RawMessage `json:"kept"`
	}
	type result struct {
		Data      json.RawMessage `json:"data,omitempty"`
		Conflicts []conflict      `json:"cf,omitempty"`
	}
	r := result{
		Data:      json.RawMessage(`{"price":3,"name":"widget"}`),
		Conflicts: []conflict{{Property: "price", Kept: json.RawMessage(`["a",2]`)}},
	}
	var buf bytes.Buffer
	f := New(true, true, &buf)
	if err := f.Print(r, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(strings.TrimSpace(out), "\n") {
		t.Errorf("minimal JSON is not one line:\n%s", out)
	}
	var doc struct {
		Data      json.RawMessage `json:"data"`
		Conflicts []conflict      `json:"cf"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !sameJSONValue(t, doc.Data, r.Data) {
		t.Errorf("data = %s, want %s (a byte array means the raw JSON was walked)", doc.Data, r.Data)
	}
	if len(doc.Conflicts) != 1 || !sameJSONValue(t, doc.Conflicts[0].Kept, r.Conflicts[0].Kept) {
		t.Errorf("cf = %s, want kept %s verbatim", out, r.Conflicts[0].Kept)
	}
	// The data's own keys are never abbreviated: "name" stays "name", not
	// the result-key abbreviation "n".
	if !strings.Contains(out, `"name":"widget"`) {
		t.Errorf("the data's own keys were abbreviated:\n%s", out)
	}
}

// Defends the other side of the raw-JSON passthrough: ordinary result keys
// are still abbreviated and empty fields still omitted under --min, and a
// nil raw message with omitempty stays out.
func TestMinimalJSONStillAbbreviatesOrdinaryKeys(t *testing.T) {
	type row struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data,omitempty"`
	}
	var buf bytes.Buffer
	f := New(true, true, &buf)
	if err := f.Print(row{Name: "x"}, nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if _, ok := doc["name"]; ok {
		t.Errorf("name was not abbreviated to n:\n%s", buf.String())
	}
	if doc["n"] != "x" {
		t.Errorf("n = %v, want x", doc["n"])
	}
	if _, ok := doc["data"]; ok {
		t.Errorf("an empty data with omitempty was printed:\n%s", buf.String())
	}
}
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	goaxi "github.com/samestrin/go-axi"
)

// schemaDoc decodes a --schema run's stdout as a document: JSON when asJSON
// (or minimal JSON), TOON otherwise. A TOON stdout that parses as JSON is a
// wrong encoding, not a pass.
func schemaDoc(t *testing.T, out string, asJSON bool) map[string]any {
	t.Helper()
	if asJSON {
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, out)
		}
		return doc
	}
	if json.Valid([]byte(out)) {
		t.Fatalf("stdout is JSON, want TOON:\n%s", out)
	}
	v, err := goaxi.Decode(strings.NewReader(out))
	if err != nil {
		t.Fatalf("stdout is not TOON: %v\n%s", err, out)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("TOON is not a document: %T", v)
	}
	return doc
}

// docJSON re-marshals a decoded document value so it can be compared with
// sameJSON regardless of the decoding's number types.
func docJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wantPromptBytes is the one prompt's size: system plus user, as sent.
func wantPromptBytes(t *testing.T, m *schemaModel) int64 {
	t.Helper()
	sent := m.sent()
	if len(sent) != 1 {
		t.Fatalf("%d requests, want 1", len(sent))
	}
	return int64(len(sent[0].message("system")) + len(sent[0].message("user")))
}

// Defends epic 13 T4: every output format carries the validated --schema
// answer as data (d in --min JSON) beside model_calls and bytes_sent, and
// --plain prints the JSON value itself. With -o the payload is in the file
// and the stdout ack drops data with the passages. Before this, no format
// carried data at all, and the answer reached a caller only as a string in
// extracted_parts. A format that drops data, prints it as a byte array, or
// counts the wrong calls turns a cell red.
func TestSchemaOutputFormatTable(t *testing.T) {
	const answer = `{"price":3}`
	modes := []struct {
		name string
		args []string
		file bool
	}{
		{name: "toon"},
		{name: "toon-min", args: []string{"--min"}},
		{name: "json", args: []string{"--json"}},
		{name: "json-min", args: []string{"--json", "--min"}},
		{name: "plain", args: []string{"--plain"}},
		{name: "plain-min", args: []string{"--plain", "--min"}},
		{name: "file-toon", file: true},
		{name: "file-plain", args: []string{"--plain"}, file: true},
		{name: "file-json", args: []string{"--json"}, file: true},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			m := newSchemaModel(t, answer)
			file := writeTemp(t, "page.txt", "The widget costs 3 dollars.")
			args := append([]string{"extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema}, mode.args...)
			var dst string
			if mode.file {
				dst = filepath.Join(t.TempDir(), "out.txt")
				args = append(args, "-o", dst)
			}
			code, out, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit %d:\n%s", code, out)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want empty", stderr)
			}
			wantBytes := wantPromptBytes(t, m)

			// The payload: stdout, or the -o file.
			payload := out
			if mode.file {
				body, err := os.ReadFile(dst)
				if err != nil {
					t.Fatal(err)
				}
				payload = string(body)
			}

			switch mode.name {
			case "toon", "file-toon":
				if mode.file {
					// The file is the plain rendering: the JSON itself.
					if payload != answer {
						t.Errorf("file = %q, want the JSON %s", payload, answer)
					}
					ack := schemaDoc(t, out, false)
					if _, ok := ack["data"]; ok {
						t.Errorf("the -o ack carries data; the payload is in the file:\n%s", out)
					}
					if _, ok := ack["extracted_parts"]; ok {
						t.Errorf("the -o ack carries extracted_parts:\n%s", out)
					}
					if docNumber(ack, "model_calls") != "1" || docNumber(ack, "total") != "1" {
						t.Errorf("ack model_calls/total = %v/%v, want 1/1:\n%s", ack["model_calls"], ack["total"], out)
					}
					return
				}
				doc := schemaDoc(t, out, false)
				if !sameJSON(t, docJSON(t, doc["data"]), []byte(answer)) {
					t.Errorf("TOON data = %v, want %s", doc["data"], answer)
				}
				if docNumber(doc, "model_calls") != "1" {
					t.Errorf("model_calls = %v, want 1", doc["model_calls"])
				}
				if docNumber(doc, "bytes_sent") != fmt.Sprint(wantBytes) {
					t.Errorf("bytes_sent = %v, want %d", doc["bytes_sent"], wantBytes)
				}
			case "json", "file-json":
				doc := schemaDoc(t, payload, true)
				if !sameJSON(t, docJSON(t, doc["data"]), []byte(answer)) {
					t.Errorf("data = %v, want %s", doc["data"], answer)
				}
				if docNumber(doc, "model_calls") != "1" {
					t.Errorf("model_calls = %v, want 1", doc["model_calls"])
				}
				if docNumber(doc, "bytes_sent") != fmt.Sprint(wantBytes) {
					t.Errorf("bytes_sent = %v, want %d", doc["bytes_sent"], wantBytes)
				}
				if mode.file {
					ack := schemaDoc(t, out, true)
					if _, ok := ack["data"]; ok {
						t.Errorf("the -o ack carries data; the payload is in the file:\n%s", out)
					}
					if _, ok := ack["extracted_parts"]; ok {
						t.Errorf("the -o ack carries extracted_parts:\n%s", out)
					}
					if docNumber(ack, "model_calls") != "1" || docNumber(ack, "total") != "1" {
						t.Errorf("ack model_calls/total = %v/%v, want 1/1:\n%s", ack["model_calls"], ack["total"], out)
					}
					return
				}
				// extracted_parts keeps the answer as text: the contract the
				// T2/T3 tests pin.
				parts, _ := doc["extracted_parts"].([]any)
				if len(parts) != 1 || parts[0] != answer {
					t.Errorf("extracted_parts = %v, want [%s]", doc["extracted_parts"], answer)
				}
			case "toon-min":
				// --min abbreviates the result keys in TOON too: d, mc, bs.
				doc := schemaDoc(t, out, false)
				if _, ok := doc["data"]; ok {
					t.Errorf("--min carries the full key data:\n%s", out)
				}
				if !sameJSON(t, docJSON(t, doc["d"]), []byte(answer)) {
					t.Errorf("TOON d = %v, want %s", doc["d"], answer)
				}
				if docNumber(doc, "mc") != "1" {
					t.Errorf("mc = %v, want 1", doc["mc"])
				}
				if docNumber(doc, "bs") != fmt.Sprint(wantBytes) {
					t.Errorf("bs = %v, want %d", doc["bs"], wantBytes)
				}
			case "json-min":
				if strings.Contains(strings.TrimSpace(out), "\n") {
					t.Errorf("--min JSON is not one line:\n%s", out)
				}
				doc := schemaDoc(t, out, true)
				if _, ok := doc["data"]; ok {
					t.Errorf("--min carries the full key data:\n%s", out)
				}
				if !sameJSON(t, docJSON(t, doc["d"]), []byte(answer)) {
					t.Errorf("d = %v, want %s (a byte array means the formatter walked the raw JSON)", doc["d"], answer)
				}
				if docNumber(doc, "mc") != "1" {
					t.Errorf("mc = %v, want 1", doc["mc"])
				}
				if docNumber(doc, "bs") != fmt.Sprint(wantBytes) {
					t.Errorf("bs = %v, want %d", doc["bs"], wantBytes)
				}
			case "plain", "plain-min":
				if strings.TrimSpace(out) != answer {
					t.Errorf("--plain stdout = %q, want the JSON %s", out, answer)
				}
				if !json.Valid([]byte(strings.TrimSpace(out))) {
					t.Errorf("--plain stdout is not JSON:\n%s", out)
				}
			case "file-plain":
				if payload != answer {
					t.Errorf("file = %q, want the JSON %s", payload, answer)
				}
				if !strings.Contains(out, "Extracted content written to") {
					t.Errorf("stdout does not say where the content went:\n%s", out)
				}
			}
		})
	}
}

// Defends the other side of data: a run without --schema extracts prose, and
// no format invents a data field for it.
func TestNoSchemaRunCarriesNoData(t *testing.T) {
	for _, format := range [][]string{{"--json"}, {"--json", "--min"}, {}} {
		newSchemaModel(t, "a passage")
		file := writeTemp(t, "page.txt", "content")
		code, out, _ := runCLI(t, append([]string{"extract-relevant", "--path", file, "--context", "x"}, format...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d:\n%s", format, code, out)
		}
		doc := schemaDoc(t, out, len(format) > 0)
		if _, ok := doc["data"]; ok {
			t.Errorf("%v: a prose run carries data:\n%s", format, out)
		}
		if _, ok := doc["d"]; ok {
			t.Errorf("%v: a prose run carries d:\n%s", format, out)
		}
	}
}

// Defends "never as data": an answer that broke --schema twice fails the run,
// and no format carries a data field beside invalid_output. A failed run's
// data would report success for an answer that never validated.
func TestSchemaFailedRunCarriesNoData(t *testing.T) {
	for _, format := range [][]string{{"--json"}, {"--json", "--min"}, {"--plain"}, {}} {
		m := newSchemaModel(t, "")
		m.respond = func(req schemaRequest) string { return "not json" }
		file := writeTemp(t, "page.txt", "content")
		code, out, _ := runCLI(t, append([]string{"extract-relevant", "--path", file, "--context", "x", "--schema", priceSchema}, format...)...)
		if code != 1 {
			t.Fatalf("%v: exit %d, want 1:\n%s", format, code, out)
		}
		if len(format) > 0 && format[0] == "--plain" {
			if !strings.HasPrefix(out, "ERROR:") || !strings.Contains(out, "INVALID OUTPUT: not json") {
				t.Errorf("--plain does not lead with the error and the invalid output:\n%s", out)
			}
			continue
		}
		doc := schemaDoc(t, out, len(format) > 0)
		dataKey, ioKey := "data", "invalid_output"
		if len(format) == 2 {
			dataKey, ioKey = "d", "io"
		}
		if _, ok := doc[dataKey]; ok {
			t.Errorf("%v: a failed run carries %s:\n%s", format, dataKey, out)
		}
		if io, _ := doc[ioKey].(string); io != "not json" {
			t.Errorf("%v: %s = %v, want the raw answer", format, ioKey, doc[ioKey])
		}
	}
}

// Defends data beside conflicts: a chunked merge keeps the first value in
// data and lists the differing ones in conflicts in every format; --plain
// prints the CONFLICT lines and the merged JSON, hiding neither.
func TestSchemaDataBesideConflicts(t *testing.T) {
	file := writeTemp(t, "big.txt", paragraphDoc(700))
	merged := func(n int) string {
		others := strings.TrimSuffix(strings.Repeat(`"b",`, n-1), ",")
		return `{"names":["a",` + others + `],"price":3}`
	}
	for _, format := range [][]string{{"--json"}, {}} {
		m := newSchemaModel(t, "")
		m.respond = chunkAnswer(`{"names":["a"],"price":3}`, `{"names":["b"],"price":4}`)
		code, out, _ := runCLI(t, append([]string{"extract-relevant", "--path", file, "--context", "names", "--schema", namesSchema}, format...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d:\n%s", format, code, out)
		}
		n := len(m.sent())
		if len(format) == 0 {
			// TOON: the data block and the conflicts are both present. The
			// nested conflicts table does not round-trip through go-axi's
			// decoder (a go-axi limitation, filed as debt), so this cell
			// reads the text; the --json cell pins the exact merged value.
			if !strings.Contains(out, "data:") || !strings.Contains(out, "conflicts[") {
				t.Errorf("TOON carries no data beside conflicts:\n%s", out)
			}
			if !strings.Contains(out, "price: 3") {
				t.Errorf("TOON data does not keep the first price:\n%s", out)
			}
			continue
		}
		doc := schemaDoc(t, out, true)
		if !sameJSON(t, docJSON(t, doc["data"]), []byte(merged(n))) {
			t.Errorf("%v: data = %v, want the merged answer %s", format, doc["data"], merged(n))
		}
		cf, _ := doc["conflicts"].([]any)
		if len(cf) != 1 {
			t.Errorf("%v: conflicts = %v, want the price conflict", format, doc["conflicts"])
		}
	}
	m := newSchemaModel(t, "")
	m.respond = chunkAnswer(`{"names":["a"],"price":3}`, `{"names":["b"],"price":4}`)
	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "names", "--schema", namesSchema, "--plain")
	if code != 0 {
		t.Fatalf("--plain: exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "CONFLICT: price: kept 3 (chunk 1)") {
		t.Errorf("--plain does not list the conflict:\n%s", out)
	}
	body := out[strings.LastIndex(out, "\n")+1:]
	if !sameJSON(t, []byte(strings.TrimSpace(body)), []byte(merged(len(m.sent())))) {
		t.Errorf("--plain does not end with the merged JSON:\n%s", out)
	}
}

// Defends the empty state under --schema: an input with no content makes no
// model call and carries no data, and says so outright instead of printing
// an empty JSON value as if the model had answered.
func TestSchemaEmptyInputCarriesNoData(t *testing.T) {
	m := newSchemaModel(t, `{"price":3}`)
	file := writeTemp(t, "empty.txt", "")
	code, out, _ := runCLI(t, "extract-relevant", "--path", file, "--context", "price", "--schema", priceSchema, "--json")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if n := len(m.sent()); n != 0 {
		t.Fatalf("%d requests, want 0 for an empty input", n)
	}
	doc := schemaDoc(t, out, true)
	if _, ok := doc["data"]; ok {
		t.Errorf("an empty input carries data:\n%s", out)
	}
	if doc["message"] != noRelevantMessage || docNumber(doc, "total") != "0" {
		t.Errorf("message/total = %v/%v, want the explicit zero result:\n%s", doc["message"], doc["total"], out)
	}
}

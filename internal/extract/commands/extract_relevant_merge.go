package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// schemaConflict is a property whose chunks gave differing non-null values:
// the merge kept the first, and the others are listed here, not hidden.
// Property is "" when the answer is not an object and the conflict is over
// the whole answer.
type schemaConflict struct {
	Property  string          `json:"property,omitempty"`
	Kept      json.RawMessage `json:"kept"`
	KeptChunk int             `json:"kept_chunk"`
	Others    []chunkValue    `json:"others"`
}

// chunkValue is one chunk's value for a property, numbered from 1.
type chunkValue struct {
	Chunk int             `json:"chunk"`
	Value json.RawMessage `json:"value"`
}

// mergeSchemaParts merges the chunks' --schema answers, in chunk order, in
// code. When every non-null answer is an object it merges per top-level
// property; otherwise the whole answer is merged as one value. A value made
// of arrays only is the arrays joined in order; any other value is the first
// non-null one, and each later non-null value that differs from it is listed
// in a conflict. A property every chunk left null stays null, and one no
// chunk gave stays out. A part that is not JSON is an error.
func mergeSchemaParts(parts []string) (string, []schemaConflict, error) {
	values := make([]json.RawMessage, len(parts))
	objects := make([]map[string]json.RawMessage, len(parts))
	allObjects, anyObject := true, false
	for i, part := range parts {
		if !json.Valid([]byte(part)) {
			return "", nil, fmt.Errorf("chunk %d's answer is not JSON", i+1)
		}
		values[i] = json.RawMessage(part)
		if isJSONNull(values[i]) {
			continue
		}
		if err := json.Unmarshal(values[i], &objects[i]); err != nil || objects[i] == nil {
			allObjects = false
		}
		anyObject = true
	}
	// Answers that are all null merge to null, not to an empty object.
	if !allObjects || !anyObject {
		merged, conflict, err := mergeValues("", values)
		if err != nil {
			return "", nil, err
		}
		return string(merged), conflictList(conflict), nil
	}

	var keys []string
	seen := map[string]bool{}
	for _, obj := range objects {
		for key := range obj {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	merged := map[string]json.RawMessage{}
	var conflicts []schemaConflict
	for _, key := range keys {
		// A chunk that left the property out gives nothing, as null does.
		column := make([]json.RawMessage, len(objects))
		for i, obj := range objects {
			column[i] = obj[key]
		}
		value, conflict, err := mergeValues(key, column)
		if err != nil {
			return "", nil, err
		}
		merged[key] = value
		conflicts = append(conflicts, conflictList(conflict)...)
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return "", nil, err
	}
	return string(out), conflicts, nil
}

// mergeValues merges one property's values, one per chunk; a nil value is
// a chunk that did not give it. It returns null when no chunk gave a
// non-null value, and a conflict, or nil, for the differing later values.
func mergeValues(property string, values []json.RawMessage) (json.RawMessage, *schemaConflict, error) {
	kept, keptAt := json.RawMessage(nil), -1
	arrays := true
	for i, v := range values {
		if v == nil || isJSONNull(v) {
			continue
		}
		if keptAt < 0 {
			kept, keptAt = v, i
		}
		if first := bytes.TrimLeft(v, " \t\r\n"); len(first) == 0 || first[0] != '[' {
			arrays = false
		}
	}
	if keptAt < 0 {
		for _, v := range values {
			if v != nil {
				return json.RawMessage("null"), nil, nil
			}
		}
		return nil, nil, nil
	}
	if arrays {
		joined := []json.RawMessage{}
		for _, v := range values {
			if v == nil || isJSONNull(v) {
				continue
			}
			var items []json.RawMessage
			if err := json.Unmarshal(v, &items); err != nil {
				return nil, nil, err
			}
			joined = append(joined, items...)
		}
		out, err := json.Marshal(joined)
		return out, nil, err
	}

	keptValue, err := decodeExact(kept)
	if err != nil {
		return nil, nil, err
	}
	conflict := &schemaConflict{Property: property, Kept: compactJSON(kept), KeptChunk: keptAt + 1}
	for i := keptAt + 1; i < len(values); i++ {
		v := values[i]
		if v == nil || isJSONNull(v) {
			continue
		}
		// Compared decoded, so key order and spacing do not make a value
		// differ.
		other, err := decodeExact(v)
		if err != nil {
			return nil, nil, err
		}
		if !reflect.DeepEqual(keptValue, other) {
			conflict.Others = append(conflict.Others, chunkValue{Chunk: i + 1, Value: compactJSON(v)})
		}
	}
	if len(conflict.Others) == 0 {
		conflict = nil
	}
	return compactJSON(kept), conflict, nil
}

// decodeExact decodes v keeping numbers as written, so two integers too
// large for a float64 to tell apart still compare as different.
func decodeExact(v json.RawMessage) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var out any
	err := dec.Decode(&out)
	return out, err
}

// conflictList is c as a list: empty when c is nil.
func conflictList(c *schemaConflict) []schemaConflict {
	if c == nil {
		return nil
	}
	return []schemaConflict{*c}
}

// isJSONNull reports whether v is the JSON null.
func isJSONNull(v json.RawMessage) bool {
	return string(bytes.TrimSpace(v)) == "null"
}

// compactJSON is v with its insignificant space removed; v is valid JSON.
func compactJSON(v json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		return v
	}
	return buf.Bytes()
}

// mergeSchemaRead merges a chunked input's --schema answers into one part
// and validates the merge again: answers that each validated can join to
// one that does not, such as arrays past maxItems. A merge that does not
// validate is an invalidOutputError, the chunks' own answers kept beside
// it. Conflicts are kept on r and named in its reduceNote.
func mergeSchemaRead(r *chunkedRead) {
	merged, conflicts, err := mergeSchemaParts(r.parts)
	if err != nil {
		r.err = fmt.Errorf("merging the %d chunk answers to --schema failed: %w", len(r.parts), err)
		return
	}
	if problem := answerProblem(merged); problem != "" {
		r.err = &invalidOutputError{output: merged, problem: problem, merged: true}
		return
	}
	r.parts, r.conflicts = []string{merged}, conflicts
	if len(conflicts) > 0 {
		r.reduceNote = fmt.Sprintf("the chunk answers to --schema were merged; %d properties had conflicting values: the first is kept and the others are listed in conflicts", len(conflicts))
	}
}

// conflictSummary renders a conflict for the text output: `price: kept 3
// (chunk 1); also 4 (chunk 2)`.
func conflictSummary(c schemaConflict) string {
	name := c.Property
	if name == "" {
		name = "the answer"
	}
	s := fmt.Sprintf("%s: kept %s (chunk %d); also", name, c.Kept, c.KeptChunk)
	for i, o := range c.Others {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf(" %s (chunk %d)", o.Value, o.Chunk)
	}
	return s
}

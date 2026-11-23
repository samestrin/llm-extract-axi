package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// jsonObject is a JSON object that keeps its keys in file order, so setup can
// merge a hook into a user's settings file without re-sorting it. Values are
// *jsonObject, []any, json.Number, string, bool or nil: numbers stay as the
// text the user wrote, so a large integer is not rounded through float64.
type jsonObject struct {
	keys []string
	vals map[string]any
}

func newJSONObject() *jsonObject { return &jsonObject{vals: map[string]any{}} }

func (o *jsonObject) get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// set replaces k's value in place, or appends k when it is new.
func (o *jsonObject) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// decodeJSONObject reads data as one JSON object, keeping key order and
// number text. A duplicate key keeps its first position and its last value,
// which is what encoding/json does with the value. Anything that is not a
// single object is an error.
func decodeJSONObject(data []byte) (*jsonObject, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the top-level value")
	}
	obj, ok := v.(*jsonObject)
	if !ok {
		return nil, errors.New("the top-level value is not an object")
	}
	return obj, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newJSONObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				obj.set(k, v)
			}
			_, err := dec.Token()
			return obj, err
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			_, err := dec.Token()
			return list, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// encodeJSON writes v compactly. Strings are not HTML-escaped, so a "<" the
// user wrote stays a "<" rather than becoming <.
func encodeJSON(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case *jsonObject:
		buf.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeJSON(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := encodeJSON(buf, t.vals[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeJSON(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(t.String())
	default:
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(t); err != nil {
			return err
		}
		buf.Write(bytes.TrimSuffix(b.Bytes(), []byte("\n")))
	}
	return nil
}

// marshalJSONObject renders obj indented with indent, ending in a newline.
func marshalJSONObject(obj *jsonObject, indent string) ([]byte, error) {
	var compact bytes.Buffer
	if err := encodeJSON(&compact, obj); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "", indent); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// detectIndent returns the indent unit of a JSON file: the leading
// whitespace of its first indented line, or two spaces when it has none.
func detectIndent(raw []byte) string {
	for _, line := range bytes.Split(raw, []byte("\n"))[1:] {
		n := len(line) - len(bytes.TrimLeft(line, " \t"))
		if n > 0 && n < len(line) {
			return string(line[:n])
		}
	}
	return "  "
}

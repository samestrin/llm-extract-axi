package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/samestrin/llm-extract-axi/v2/pkg/llmapi"
	"github.com/samestrin/llm-extract-axi/v2/pkg/output"
)

// extractRelevantSchema is the --schema value: a JSON Schema inline, or the
// path of a file holding one.
var extractRelevantSchema string

// relevantSchema is the run's checked --schema, compacted, or nil when the
// run has none. runExtractRelevant sets it before any call.
var relevantSchema json.RawMessage

// maxSchemaFileBytes bounds the --schema file read. The compacted schema must
// fit one prompt anyway; this only stops a huge file being read whole.
const maxSchemaFileBytes = 1 << 20

// relevantSchemaName is the name response_format gives the schema.
const relevantSchemaName = "extraction"

// relevantSchemaSystemPrompt replaces the prose extraction prompt under
// --schema: the answer is one JSON value, not passages.
const relevantSchemaSystemPrompt = `You are a structured extraction assistant. Your task is to extract, from the provided content, the data relevant to the given context.

Rules:
1. Answer with one JSON value that validates against the JSON Schema given in the message, and nothing else: no prose and no code fences
2. Take every value from the content; do not invent values
3. Where the content has no value for a field the schema allows to be null or left out, use null or leave it out`

// loadSchema reads and checks a --schema value. A value that starts with "{"
// once trimmed is the schema itself; anything else is a file path. The schema
// must be JSON with an object root, and every $ref and $dynamicRef in it must
// be local ("#..."): a remote or file reference would make the run read or
// fetch something the caller never named. Every refusal is a usage error, as
// the same value never works; it returns the schema compacted.
func loadSchema(value string) (json.RawMessage, error) {
	refuse := func(format string, a ...any) error {
		return output.UsageError(fmt.Errorf("--schema: "+format, a...))
	}
	data := []byte(value)
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		if value == "" {
			return nil, refuse("empty; give a JSON Schema inline or the path of a file holding one")
		}
		info, err := os.Stat(value)
		if err != nil {
			return nil, refuse("cannot read schema file: %v", err)
		}
		// Reading a FIFO blocks until a writer appears, and /dev/zero never
		// ends.
		if !info.Mode().IsRegular() {
			return nil, refuse("cannot read schema file %s: not a regular file", value)
		}
		if data, err = readCapped(value, maxSchemaFileBytes); err != nil {
			return nil, refuse("cannot read schema file: %v", err)
		}
		if len(data) > maxSchemaFileBytes {
			return nil, refuse("schema file %s is over %d bytes", value, maxSchemaFileBytes)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, refuse("not valid JSON: %v", err)
	}
	// Every prompt carries the schema, so one that fills a prompt leaves no
	// room for content.
	if compact.Len() >= maxRelevantPromptBytes {
		return nil, refuse("the schema is %d bytes or more; it must fit in one %d-byte prompt with room for content", compact.Len(), maxRelevantPromptBytes)
	}
	var root any
	if err := json.Unmarshal(compact.Bytes(), &root); err != nil {
		return nil, refuse("not valid JSON: %v", err)
	}
	if _, ok := root.(map[string]any); !ok {
		return nil, refuse("the schema's root must be a JSON object, not %s", jsonKind(root))
	}
	if err := checkSchemaRefs(root); err != nil {
		return nil, output.UsageError(fmt.Errorf("--schema: %w", err))
	}
	return compact.Bytes(), nil
}

// jsonKind names the JSON type of a decoded value.
func jsonKind(v any) string {
	switch v.(type) {
	case []any:
		return "an array"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case float64:
		return "a number"
	case nil:
		return "null"
	}
	return "an object"
}

// checkSchemaRefs walks a decoded schema and refuses any $ref or $dynamicRef
// that is not local. The keys under properties, $defs and the like are names,
// not keywords, so a property called "$ref" is a schema; and enum, const,
// default and examples hold data, so a "$ref" in them is not a reference.
func checkSchemaRefs(v any) error {
	switch v := v.(type) {
	case []any:
		for _, item := range v {
			if err := checkSchemaRefs(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, val := range v {
			switch key {
			case "$ref", "$dynamicRef":
				if err := checkLocalRef(key, val); err != nil {
					return err
				}
			case "enum", "const", "default", "examples":
			case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
				if named, ok := val.(map[string]any); ok {
					for _, schema := range named {
						if err := checkSchemaRefs(schema); err != nil {
							return err
						}
					}
				}
			default:
				if err := checkSchemaRefs(val); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkLocalRef refuses a reference that is not a string starting with "#".
func checkLocalRef(key string, val any) error {
	ref, ok := val.(string)
	if !ok {
		return fmt.Errorf("%s must be a string, not %s", key, jsonKind(val))
	}
	if strings.HasPrefix(ref, "#") {
		return nil
	}
	u, err := url.Parse(ref)
	switch {
	case err == nil && (u.Scheme == "http" || u.Scheme == "https"):
		return fmt.Errorf("%s %q is remote; only local references (\"#...\") are allowed, and nothing is fetched", key, ref)
	case err == nil && u.Scheme != "":
		return fmt.Errorf("%s %q uses the %s: scheme; only local references (\"#...\") are allowed", key, ref, u.Scheme)
	}
	return fmt.Errorf("%s %q points at another file; only local references (\"#...\") are allowed, and no file is read", key, ref)
}

// checkSchemaRun loads --schema when it was given and refuses it with a
// directory --path: a directory's files each answer on their own, and their
// answers have no rule to merge by. It sets relevantSchema, or clears it
// when there is no --schema.
func checkSchemaRun(given bool, targetPath string) error {
	relevantSchema = nil
	if !given {
		return nil
	}
	schema, err := loadSchema(extractRelevantSchema)
	if err != nil {
		return err
	}
	if !isURL(targetPath) {
		if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
			return output.UsageError(errors.New("--schema cannot be used with a directory --path; name one file or URL"))
		}
	}
	relevantSchema = schema
	return nil
}

// relevantFormat is the response_format the run's map calls send, or nil
// without --schema.
func relevantFormat() *llmapi.ResponseFormat {
	if relevantSchema == nil {
		return nil
	}
	return llmapi.JSONSchemaFormat(relevantSchemaName, relevantSchema)
}

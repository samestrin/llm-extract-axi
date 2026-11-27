package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

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

// relevantResolved is relevantSchema ready to validate answers against, set
// and cleared with it.
var relevantResolved *jsonschema.Resolved

// draft202012 is the only $schema the validator checks against.
const draft202012 = "https://json-schema.org/draft/2020-12/schema"

// relevantSchemaRetryLead opens the feedback the retry adds to the prompt.
const relevantSchemaRetryLead = "Your previous answer did not validate against the JSON Schema"

// schemaFeedbackBytes is the room every --schema prompt leaves for the
// retry's feedback: the chunker sizes chunks this much smaller, and the
// feedback is cut to fit it, so the retry fits maxRelevantPromptBytes too.
const schemaFeedbackBytes = 2000

// schemaFeedbackCut marks validation errors cut to fit schemaFeedbackBytes.
const schemaFeedbackCut = " [cut]"

// invalidOutputError is an answer that still broke --schema after the retry.
// output is the raw answer, for the result's invalid_output.
// merged says it is the chunks' answers merged, each of which validated.
type invalidOutputError struct {
	output, problem string
	merged          bool
}

func (e *invalidOutputError) Error() string {
	if e.merged {
		return "the merged answer of the chunks does not validate against --schema: " + e.problem
	}
	return "the answer does not validate against --schema after one retry: " + e.problem
}

// resolveSchema prepares a checked schema for validation. The validator
// checks draft 2020-12 only, so another $schema is refused, as is a schema
// it cannot resolve: an answer that could never be checked is not asked for.
func resolveSchema(schema json.RawMessage) (*jsonschema.Resolved, error) {
	refuse := func(err error) error {
		return output.UsageError(fmt.Errorf("--schema: not a draft 2020-12 JSON Schema the validator can use: %w", err))
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, refuse(err)
	}
	if s.Schema != "" && s.Schema != draft202012 {
		return nil, refuse(fmt.Errorf("$schema is %q", s.Schema))
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return nil, refuse(err)
	}
	return resolved, nil
}

// answerProblem returns why answer does not validate against the run's
// schema, or "" when it does. An answer that is not one JSON value, NONE and
// an empty answer included, does not validate.
func answerProblem(answer string) string {
	var v any
	if err := json.Unmarshal([]byte(answer), &v); err != nil {
		return fmt.Sprintf("the answer is not JSON: %v", err)
	}
	if err := relevantResolved.Validate(v); err != nil {
		return err.Error()
	}
	return ""
}

// schemaFeedback is what the retry adds to the first prompt: the validation
// errors, cut to fit schemaFeedbackBytes.
func schemaFeedback(problem string) string {
	lead := "\n\n---\n\n" + relevantSchemaRetryLead + ":\n"
	tail := "\n\nAnswer again with one JSON value that validates against the schema, and nothing else."
	if room := schemaFeedbackBytes - len(lead) - len(tail); len(problem) > room {
		problem = cutAtRuneBoundary(problem, room-len(schemaFeedbackCut)) + schemaFeedbackCut
	}
	return lead + problem + tail
}

// sendSchemaPrompt makes a --schema extraction call and validates the answer.
// An invalid answer is asked for once more, the validation errors added to
// the prompt; an answer invalid again is an invalidOutputError, never
// returned as data. Each call is counted in model_calls.
func sendSchemaPrompt(ctx context.Context, client *llmapi.LLMClient, systemPrompt, userPrompt string, timeout int) (string, error) {
	answer, err := sendRelevantPrompt(ctx, client, systemPrompt, userPrompt, timeout, relevantFormat())
	if err != nil {
		return "", err
	}
	problem := answerProblem(answer)
	if problem == "" {
		return answer, nil
	}
	answer, err = sendRelevantPrompt(ctx, client, systemPrompt, userPrompt+schemaFeedback(problem), timeout, relevantFormat())
	if err != nil {
		return "", fmt.Errorf("the retry after an answer that did not validate against --schema failed: %w", err)
	}
	if problem := answerProblem(answer); problem != "" {
		return "", &invalidOutputError{output: answer, problem: problem}
	}
	return answer, nil
}

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
	// Every prompt carries the schema and leaves room for the retry's
	// feedback, so a schema that fills the rest leaves no room for content.
	if compact.Len() >= maxRelevantPromptBytes-schemaFeedbackBytes {
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
// when there is no --schema, and relevantResolved with it.
func checkSchemaRun(given bool, targetPath string) error {
	relevantSchema, relevantResolved = nil, nil
	if !given {
		return nil
	}
	schema, err := loadSchema(extractRelevantSchema)
	if err != nil {
		return err
	}
	resolved, err := resolveSchema(schema)
	if err != nil {
		return err
	}
	if !isURL(targetPath) {
		if info, err := os.Stat(targetPath); err == nil && info.IsDir() {
			return output.UsageError(errors.New("--schema cannot be used with a directory --path; name one file or URL"))
		}
	}
	relevantSchema, relevantResolved = schema, resolved
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

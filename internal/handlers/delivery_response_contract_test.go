package handlers

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Check the operation's status map before validating its declared body schema.
// A generic ErrorEnvelope check alone accepts undocumented status codes.
func deliveryResponseContract(t *testing.T, spec map[string]any, compiler *jsonschema.Compiler, base, path, method string, status int, body []byte) {
	t.Helper()
	operation := spec["paths"].(map[string]any)[path].(map[string]any)[strings.ToLower(method)].(map[string]any)
	responses := operation["responses"].(map[string]any)
	key := fmt.Sprint(status)
	response, ok := responses[key]
	if !ok {
		key = "default"
		response, ok = responses[key]
	}
	if !ok {
		t.Errorf("own OpenAPI %s %s does not declare HTTP%d: %s", method, path, status, body)
		return
	}
	pointer := "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/" + strings.ToLower(method) + "/responses/" + key
	if ref, ok := response.(map[string]any)["$ref"].(string); ok {
		pointer = ref
	}
	schema, err := compiler.Compile(base + pointer + "/content/application~1json/schema")
	historyCheck(t, err)
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	historyCheck(t, err)
	historyCheck(t, schema.Validate(value))
}

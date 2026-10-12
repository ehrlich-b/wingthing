package runinspect

import (
	"encoding/json"
	"testing"
)

func TestRunInspectJSONSchemaKeySemantics(t *testing.T) {
	type item struct {
		Status string `json:"status"`
	}
	type record struct {
		Item    *item             `json:"item"`
		Items   []item            `json:"items"`
		Indexed map[string]item   `json:"indexed"`
		Env     map[string]string `json:"env"`
		Raw     json.RawMessage   `json:"raw"`
	}
	for _, test := range []struct {
		name, data string
		valid      bool
	}{
		{"case_sensitive_map", `{"env":{"FOO":"upper","foo":"lower"}}`, true},
		{"case_sensitive_raw", `{"raw":{"FOO":1,"foo":2}}`, true},
		{"case_sensitive_unknown", `{"unknown":{"FOO":1,"foo":2}}`, true},
		{"case_sensitive_map_with_struct_values", `{"indexed":{"FOO":{"status":"done"},"foo":{"status":"failed"}}}`, true},
		{"pointer_field_alias", `{"item":{"status":"done","Status":"failed"}}`, false},
		{"slice_field_alias", `{"items":[{"status":"done","Status":"failed"}]}`, false},
		{"map_value_field_alias", `{"indexed":{"FOO":{"status":"done","Status":"failed"}}}`, false},
		{"duplicate_map_key", `{"env":{"FOO":"upper","FOO":"lower"}}`, false},
		{"duplicate_raw_key", `{"raw":{"FOO":1,"FOO":2}}`, false},
		{"duplicate_unknown_key", `{"unknown":{"FOO":1,"FOO":2}}`, false},
		{"escaped_duplicate", `{"env":{"FOO":"upper","\u0046OO":"lower"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var decoded record
			if err := decodeRecord([]byte(test.data), &decoded); (err == nil) != test.valid {
				t.Fatalf("valid=%v, want %v: %v", err == nil, test.valid, err)
			}
		})
	}
	// Distinct exact tags remain distinct even when the folded names match.
	var exact struct {
		Upper string `json:"FOO"`
		Lower string `json:"foo"`
	}
	if err := decodeRecord([]byte(`{"FOO":"upper","foo":"lower"}`), &exact); err != nil || exact.Upper != "upper" || exact.Lower != "lower" {
		t.Fatalf("exact field precedence differs from encoding/json: %+v %v", exact, err)
	}
}

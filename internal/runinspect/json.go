package runinspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Exact duplicate keys are always ambiguous. Case aliases are ambiguous only
// when they decode into the same struct field: maps and uninterpreted native
// payloads retain JSON's case-sensitive keys. A nil target checks exact keys.
func uniqueJSON(data []byte, target any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid UTF-8 record")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int, reflect.Type) error
	value = func(depth int, typ reflect.Type) error {
		if depth > 128 {
			return errors.New("record nesting exceeds limit")
		}
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				seenFields := map[string]bool{}
				exact, folded := jsonObjectFields(typ)
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					if !ok || seen[name] {
						return errors.New("ambiguous object keys")
					}
					seen[name] = true
					var child reflect.Type
					field, known := exact[name]
					if !known {
						field, known = folded[foldedJSONKey(name)]
					}
					if known {
						if seenFields[field.Name] {
							return errors.New("ambiguous struct field aliases")
						}
						seenFields[field.Name] = true
						child = field.Type
					} else if typ != nil && typ.Kind() == reflect.Map {
						child = typ.Elem()
					}
					if err := value(depth+1, child); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return errors.New("invalid object")
				}
			case '[':
				var child reflect.Type
				if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
					child = typ.Elem()
				}
				for decoder.More() {
					if err := value(depth+1, child); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim(']') {
					return errors.New("invalid array")
				}
			default:
				return errors.New("invalid JSON delimiter")
			}
		}
		return nil
	}
	if err := value(0, reflect.TypeOf(target)); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("record has trailing data")
	}
	return nil
}

// Inspector projections and SessionEvent use explicit, non-embedded fields.
// Prefer an exact tag/name before the first folded match, as encoding/json does.
func jsonObjectFields(typ reflect.Type) (exact, folded map[string]reflect.StructField) {
	if typ == nil || typ.Kind() != reflect.Struct {
		return nil, nil
	}
	exact = map[string]reflect.StructField{}
	folded = map[string]reflect.StructField{}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if field.PkgPath != "" || name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		exact[name] = field
		key := foldedJSONKey(name)
		if _, exists := folded[key]; !exists {
			folded[key] = field
		}
	}
	return exact, folded
}

// Use the same equivalence as encoding/json (including K/kelvin and S/long S),
// while preserving legitimate single capitalized writer fields like Authority.
func foldedJSONKey(name string) string {
	return strings.Map(func(r rune) rune {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		return minimum
	}, name)
}

func decodeRecord(data []byte, target any) error {
	if err := uniqueJSON(data, target); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

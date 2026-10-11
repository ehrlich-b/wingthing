package runinspect

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Duplicate keys, including case aliases, are ambiguous evidence. encoding/json
// matches struct fields using Unicode simple folding and keeps the last
// value, which would silently pick an identity or ownership winner.
func uniqueJSON(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("invalid UTF-8 record")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return errors.New("record nesting exceeds limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					if err != nil {
						return err
					}
					name, ok := key.(string)
					name = foldedJSONKey(name)
					if !ok || seen[name] {
						return errors.New("ambiguous object keys")
					}
					seen[name] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, err := decoder.Token()
				if err != nil || end != json.Delim('}') {
					return errors.New("invalid object")
				}
			case '[':
				for decoder.More() {
					if err := value(depth + 1); err != nil {
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
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("record has trailing data")
	}
	return nil
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
	if err := uniqueJSON(data); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

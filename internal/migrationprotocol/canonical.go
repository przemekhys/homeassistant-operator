// Package migrationprotocol defines local, versioned execution evidence rules.
// It never contacts Home Assistant or dispatches work.
package migrationprotocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidDigest(s string) bool { return digestPattern.MatchString(s) }

// CanonicalJSON sorts object keys, preserves Unicode, and rejects duplicate keys.
// Protocol inputs contain no fractional numbers; unknown fields are rejected by Decode.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("invalid protocol JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := value(d, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid protocol JSON")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("invalid protocol JSON")
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, fmt.Errorf("invalid protocol JSON")
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

func value(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("JSON nesting limit")
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, err
			}
			s, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("invalid key")
			}
			if _, exists := m[s]; exists {
				return nil, fmt.Errorf("duplicate key")
			}
			v, err := value(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[s] = v
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, fmt.Errorf("invalid object")
		}
		return m, nil
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := value(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, fmt.Errorf("invalid array")
		}
		return a, nil
	}
	if _, ok := t.(json.Delim); ok {
		return nil, fmt.Errorf("unexpected delimiter")
	}
	if n, ok := t.(json.Number); ok {
		if _, err := n.Int64(); err != nil {
			return nil, fmt.Errorf("non-integer number")
		}
	}
	return t, nil
}

// Decode rejects unknown fields, duplicate keys and trailing input without echoing data.
func Decode(raw []byte, dst any) error {
	b, err := CanonicalJSON(raw)
	if err != nil {
		return err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil || containsNull(generic) {
		return fmt.Errorf("invalid protocol fields")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("invalid protocol fields")
	}
	return nil
}

func containsNull(v any) bool {
	if v == nil {
		return true
	}
	switch value := v.(type) {
	case map[string]any:
		for _, item := range value {
			if containsNull(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if containsNull(item) {
				return true
			}
		}
	}
	return false
}

func Canonical(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("invalid protocol value")
	}
	return CanonicalJSON(b)
}

func Digest(v any) (string, error) {
	b, err := Canonical(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

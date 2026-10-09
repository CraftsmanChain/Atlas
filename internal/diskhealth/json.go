package diskhealth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
)

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Reject duplicate keys before decoding: conflicting health fields must not use last-value-wins.
func ValidateJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var visit func(int) error
	visit = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("JSON nesting exceeds 32")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("invalid or duplicate JSON key")
				}
				seen[name] = true
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := visit(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := visit(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

type object map[string]json.RawMessage

func decodeObject(raw []byte) (object, error) {
	var out object
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, fmt.Errorf("expected JSON object")
	}
	return out, nil
}
func (o object) child(key string) object { out, _ := decodeObject(o[key]); return out }
func (o object) text(key string) string  { var out string; _ = json.Unmarshal(o[key], &out); return out }

func exactInteger(raw json.RawMessage, bits int) (*big.Int, error) {
	text := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
	}
	if len(text) == 0 || len(text) > 40 {
		return nil, fmt.Errorf("missing or invalid integer")
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("expected unsigned decimal integer")
		}
	}
	value, ok := new(big.Int).SetString(text, 10)
	if !ok || value.BitLen() > bits {
		return nil, fmt.Errorf("integer outside %d-bit range", bits)
	}
	return value, nil
}

func integer(o object, field string, bits int) (*big.Int, error) {
	value, err := decimalRepresentation(o, field, bits)
	if err != nil {
		return nil, err
	}
	if raw, exists := o[field+"_le"]; exists {
		var octets []json.RawMessage
		if json.Unmarshal(raw, &octets) != nil || len(octets) == 0 || len(octets) > 16 {
			return nil, fmt.Errorf("invalid little endian representation")
		}
		decoded := new(big.Int)
		for i, octet := range octets {
			part, err := exactInteger(octet, 8)
			if err != nil {
				return nil, err
			}
			decoded.Add(decoded, part.Lsh(part, uint(8*i)))
		}
		if decoded.Cmp(value) != 0 {
			return nil, fmt.Errorf("conflicting little endian representation")
		}
	}
	return value, nil
}

func decimalRepresentation(o object, field string, bits int) (*big.Int, error) {
	// smartctl JSON v supplies exact decimal *_s values for counters unsafe in JavaScript.
	if raw, ok := o[field+"_s"]; ok {
		value, err := exactInteger(raw, bits)
		if err != nil {
			return nil, err
		}
		if numeric, exists := o[field]; exists {
			other, e := exactInteger(numeric, bits)
			if e != nil || other.Cmp(value) != 0 {
				return nil, fmt.Errorf("conflicting numeric representations")
			}
		}
		return value, nil
	}
	return exactInteger(o[field], bits)
}

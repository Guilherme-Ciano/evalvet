package cache

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
)

func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical marshal: %w", err)
	}
	return CanonicalJSON(raw)
}

func CanonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical decode: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("canonical decode: %w", errTrailing)
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(x))
	case json.Number:
		s, err := normalizeNumber(x)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case string:
		writeString(buf, x)
	case []any:
		return writeArray(buf, x)
	case map[string]any:
		return writeObject(buf, x)
	default:
		return fmt.Errorf("canonical: unexpected type %T: %w", v, errUnexpected)
	}
	return nil
}

func writeArray(buf *bytes.Buffer, x []any) error {
	buf.WriteByte('[')
	for i, e := range x {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeCanonical(buf, e); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

func writeObject(buf *bytes.Buffer, x map[string]any) error {
	keys := slices.Sorted(maps.Keys(x))
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeString(buf, k)
		buf.WriteByte(':')
		if err := writeCanonical(buf, x[k]); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func normalizeNumber(n json.Number) (string, error) {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return strconv.FormatInt(i, 10), nil
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		return "", fmt.Errorf("canonical number %q: %w", n, err)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("canonical number %q: %w", n, errNonFinite)
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10), nil
	}
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}

func writeString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	start := buf.Len()
	if err := enc.Encode(s); err != nil {
		buf.Truncate(start)
		buf.WriteString(strconv.Quote(s))
		return
	}
	buf.Truncate(buf.Len() - 1)
}

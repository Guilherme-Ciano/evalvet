package assert

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

const maxJSONAttempts = 16

var errNoJSON = errors.New("no JSON value found in output")

func ExtractJSON(s string) (any, error) {
	s = strings.TrimSpace(s)
	if v, err := decodeFirst(s); err == nil {
		return v, nil
	}
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			if v, err := decodeFirst(rest[:end]); err == nil {
				return v, nil
			}
		}
	}
	attempts := 0
	for i, r := range s {
		if r != '{' && r != '[' {
			continue
		}
		if attempts == maxJSONAttempts {
			break
		}
		attempts++
		if v, err := decodeFirst(s[i:]); err == nil {
			return v, nil
		}
	}
	return nil, errNoJSON
}

func decodeFirst(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	switch v.(type) {
	case map[string]any, []any:
		return v, nil
	}
	return nil, errNoJSON
}

func valueStrings(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("list values must be strings, got %T: %w", e, domain.ErrConfig)
			}
			out = append(out, s)
		}
		return out, nil
	case nil:
		return nil, fmt.Errorf("value is required: %w", domain.ErrConfig)
	default:
		return []string{fmt.Sprint(x)}, nil
	}
}

func compileEquals(spec config.AssertSpec) (Assertion, error) {
	if s, ok := spec.Value.(string); ok {
		return simple("equals", func(in Input) Result {
			got := strings.TrimSpace(in.Output)
			want := strings.TrimSpace(s)
			if spec.CaseInsensitive {
				got, want = strings.ToLower(got), strings.ToLower(want)
			}
			if got != want {
				return fail("equals", "output %q != %q", clip(got), clip(want))
			}
			return pass("equals")
		}), nil
	}
	want, err := cache.Canonical(spec.Value)
	if err != nil {
		return nil, fmt.Errorf("equals value: %w", err)
	}
	return simple("equals", func(in Input) Result {
		v, err := ExtractJSON(in.Output)
		if err != nil {
			return fail("equals", "%v", err)
		}
		got, err := cache.Canonical(v)
		if err != nil || !bytes.Equal(got, want) {
			return fail("equals", "JSON %s != %s", clip(string(got)), clip(string(want)))
		}
		return pass("equals")
	}), nil
}

func compileContains(spec config.AssertSpec) (Assertion, error) {
	values, err := valueStrings(spec.Value)
	if err != nil {
		return nil, err
	}
	needles := values
	if spec.CaseInsensitive {
		needles = make([]string, len(values))
		for i, v := range values {
			needles[i] = strings.ToLower(v)
		}
	}
	return simple("contains", func(in Input) Result {
		hay := in.Output
		if spec.CaseInsensitive {
			hay = strings.ToLower(hay)
		}
		var missing []string
		found := 0
		for i, n := range needles {
			if strings.Contains(hay, n) {
				found++
			} else {
				missing = append(missing, values[i])
			}
		}
		if spec.Any && found > 0 || !spec.Any && len(missing) == 0 {
			return pass("contains")
		}
		if spec.Any {
			return fail("contains", "none of %q found", values)
		}
		return fail("contains", "missing %q", missing)
	}), nil
}

func compileRegex(spec config.AssertSpec) (Assertion, error) {
	pattern := spec.Pattern
	if pattern == "" {
		s, ok := spec.Value.(string)
		if !ok {
			return nil, fmt.Errorf("regex pattern must be a string: %w", domain.ErrConfig)
		}
		pattern = s
	}
	if spec.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("regex %q: %w: %w", pattern, err, domain.ErrConfig)
	}
	return simple("regex", func(in Input) Result {
		if !re.MatchString(in.Output) {
			return fail("regex", "output does not match %s", pattern)
		}
		return pass("regex")
	}), nil
}

func clip(s string) string {
	const n = 120
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

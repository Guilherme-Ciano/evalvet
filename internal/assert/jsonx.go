package assert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"

	"github.com/ohler55/ojg/jp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
	"github.com/Guilherme-Ciano/evalvet/internal/match"
)

func normalize(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	out, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("normalize: %w", err)
	}
	return out, nil
}

func compileJSONSchema(spec config.AssertSpec) (Assertion, error) {
	doc, err := normalize(spec.Schema)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	const loc = "evalvet://schema.json"
	if err := c.AddResource(loc, doc); err != nil {
		return nil, fmt.Errorf("json_schema: %w: %w", err, domain.ErrConfig)
	}
	sch, err := c.Compile(loc)
	if err != nil {
		return nil, fmt.Errorf("json_schema: %w: %w", err, domain.ErrConfig)
	}
	return simple("json_schema", func(in Input) Result {
		v, err := ExtractJSON(in.Output)
		if err != nil {
			return fail("json_schema", "%v", err)
		}
		inst, err := normalize(v)
		if err != nil {
			return fail("json_schema", "%v", err)
		}
		if err := sch.Validate(inst); err != nil {
			return fail("json_schema", "%s", firstLine(err.Error()))
		}
		return pass("json_schema")
	}), nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

func checkPath(spec config.AssertSpec, re *regexp.Regexp, results []any) Result {
	const kind = "json_path"
	if spec.Exists != nil && !*spec.Exists {
		if len(results) > 0 {
			return fail(kind, "%s exists", spec.Path)
		}
		return pass(kind)
	}
	if len(results) == 0 {
		return fail(kind, "%s not found", spec.Path)
	}
	switch {
	case spec.Equals != nil:
		if slices.ContainsFunc(results, func(r any) bool { return match.Equal(r, spec.Equals) }) {
			return pass(kind)
		}
		return fail(kind, "%s: %v ≠ %v", spec.Path, results[0], spec.Equals)
	case re != nil:
		ok := slices.ContainsFunc(results, func(r any) bool {
			s, isStr := r.(string)
			return isStr && re.MatchString(s)
		})
		if ok {
			return pass(kind)
		}
		return fail(kind, "%s: %v does not match %s", spec.Path, results[0], spec.Regex)
	}
	return pass(kind)
}

func compileJSONPath(spec config.AssertSpec) (Assertion, error) {
	expr, err := jp.ParseString(spec.Path)
	if err != nil {
		return nil, fmt.Errorf("json_path %q: %w: %w", spec.Path, err, domain.ErrConfig)
	}
	var re *regexp.Regexp
	if spec.Regex != "" {
		if re, err = regexp.Compile(spec.Regex); err != nil {
			return nil, fmt.Errorf("json_path regex %q: %w: %w", spec.Regex, err, domain.ErrConfig)
		}
	}
	return simple("json_path", func(in Input) Result {
		v, err := ExtractJSON(in.Output)
		if err != nil {
			return fail("json_path", "%v", err)
		}
		plain, err := plainJSON(v)
		if err != nil {
			return fail("json_path", "%v", err)
		}
		return checkPath(spec, re, expr.Get(plain))
	}), nil
}

func plainJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("re-encode: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("re-decode: %w", err)
	}
	return out, nil
}

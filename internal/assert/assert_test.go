package assert_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/assert"
	"github.com/Guilherme-Ciano/evalvet/internal/config"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func out(s string) assert.Input { return assert.Input{Output: s} }

func toolStep(calls ...domain.ToolCall) domain.Step {
	return domain.Step{Response: domain.Response{ToolCalls: calls, StopReason: domain.StopToolUse}}
}

func tc(name, args string) domain.ToolCall {
	return domain.ToolCall{ID: name, Name: name, Arguments: json.RawMessage(args)}
}

func traj(steps ...domain.Step) assert.Input {
	return assert.Input{Trajectory: domain.Trajectory{Steps: steps}, Agent: true}
}

type check struct {
	name string
	spec config.AssertSpec
	in   assert.Input
	pass bool
}

func runChecks(t *testing.T, tests []check) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := assert.Compile(tt.spec, assert.Deps{})
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			r, err := a.Evaluate(context.Background(), tt.in)
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if r.Pass != tt.pass {
				t.Fatalf("pass = %v, want %v (reason %q)", r.Pass, tt.pass, r.Reason)
			}
			if !r.Pass && r.Reason == "" {
				t.Fatal("failing result must carry a reason")
			}
			if r.Pass && r.Reason != "" {
				t.Fatalf("passing result has reason %q", r.Reason)
			}
		})
	}
}

func TestEquals(t *testing.T) {
	t.Parallel()
	runChecks(t, []check{
		{"string exact", config.AssertSpec{Kind: "equals", Value: "yes"}, out("yes"), true},
		{"string trims whitespace", config.AssertSpec{Kind: "equals", Value: " yes\n"}, out("\n yes "), true},
		{"string mismatch", config.AssertSpec{Kind: "equals", Value: "yes"}, out("no"), false},
		{"string case sensitive by default", config.AssertSpec{Kind: "equals", Value: "Yes"}, out("yes"), false},
		{
			"string case insensitive",
			config.AssertSpec{Kind: "equals", Value: "Yes", CaseInsensitive: true},
			out("YES"),
			true,
		},
		{
			"unicode case insensitive",
			config.AssertSpec{Kind: "equals", Value: "ÉCOLE", CaseInsensitive: true},
			out("école"),
			true,
		},
		{
			"long mismatch is clipped but fails",
			config.AssertSpec{Kind: "equals", Value: "a"},
			out(strings.Repeat("b", 5000)),
			false,
		},
		{
			"json object key order and number type",
			config.AssertSpec{Kind: "equals", Value: map[string]any{"a": 1, "b": []any{"x"}}},
			out(`{"b":["x"],"a":1.0}`),
			true,
		},
		{
			"json object inside prose",
			config.AssertSpec{Kind: "equals", Value: map[string]any{"a": 1}},
			out("Here you go: {\"a\": 1} thanks"),
			true,
		},
		{
			"json object differs",
			config.AssertSpec{Kind: "equals", Value: map[string]any{"a": 1}},
			out(`{"a":2}`),
			false,
		},
		{
			"json value but output not json",
			config.AssertSpec{Kind: "equals", Value: map[string]any{"a": 1}},
			out("nope"),
			false,
		},
		{"json array", config.AssertSpec{Kind: "equals", Value: []any{1, 2}}, out("[1,2]"), true},
	})
}

func TestContains(t *testing.T) {
	t.Parallel()
	runChecks(t, []check{
		{"single string present", config.AssertSpec{Kind: "contains", Value: "ref"}, out("a refund"), true},
		{"single string absent", config.AssertSpec{Kind: "contains", Value: "xyz"}, out("a refund"), false},
		{"all present", config.AssertSpec{Kind: "contains", Value: []any{"a", "b"}}, out("ab"), true},
		{"all one missing", config.AssertSpec{Kind: "contains", Value: []any{"a", "z"}}, out("ab"), false},
		{"any one present", config.AssertSpec{Kind: "contains", Value: []any{"z", "b"}, Any: true}, out("ab"), true},
		{"any none present", config.AssertSpec{Kind: "contains", Value: []any{"z", "y"}, Any: true}, out("ab"), false},
		{"case sensitive by default", config.AssertSpec{Kind: "contains", Value: "REFUND"}, out("refund"), false},
		{
			"case insensitive",
			config.AssertSpec{Kind: "contains", Value: "REFUND", CaseInsensitive: true},
			out("Refund"),
			true,
		},
		{"unicode", config.AssertSpec{Kind: "contains", Value: "日本語"}, out("これは日本語です"), true},
		{
			"unicode case insensitive",
			config.AssertSpec{Kind: "contains", Value: "ÜBER", CaseInsensitive: true},
			out("das ist über"),
			true,
		},
		{"emoji", config.AssertSpec{Kind: "contains", Value: "👍"}, out("ok 👍"), true},
		{"number value stringified", config.AssertSpec{Kind: "contains", Value: 1499}, out("price 1499 usd"), true},
		{
			"huge output",
			config.AssertSpec{Kind: "contains", Value: "needle"},
			out(strings.Repeat("hay ", 2_000_000) + "needle"),
			true,
		},
		{
			"huge output absent",
			config.AssertSpec{Kind: "contains", Value: "needle"},
			out(strings.Repeat("hay ", 2_000_000)),
			false,
		},
		{"empty list all passes vacuously", config.AssertSpec{Kind: "contains", Value: []any{}}, out("x"), true},
		{"empty list any fails", config.AssertSpec{Kind: "contains", Value: []any{}, Any: true}, out("x"), false},
	})
}

func TestContainsCompileErrors(t *testing.T) {
	t.Parallel()
	for name, v := range map[string]any{
		"nil":             nil,
		"non-string list": []any{"a", 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := assert.Compile(config.AssertSpec{Kind: "contains", Value: v}, assert.Deps{})
			if !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRegex(t *testing.T) {
	t.Parallel()
	runChecks(t, []check{
		{"pattern match", config.AssertSpec{Kind: "regex", Pattern: `^\d{3}$`}, out("123"), true},
		{"pattern no match", config.AssertSpec{Kind: "regex", Pattern: `^\d{3}$`}, out("12a"), false},
		{"value fallback", config.AssertSpec{Kind: "regex", Value: `a+b`}, out("xaab"), true},
		{
			"case insensitive flag",
			config.AssertSpec{Kind: "regex", Pattern: `hello`, CaseInsensitive: true},
			out("HELLO"),
			true,
		},
		{"unicode class", config.AssertSpec{Kind: "regex", Pattern: `\p{Han}+`}, out("漢字"), true},
		{
			"huge input linear time",
			config.AssertSpec{Kind: "regex", Pattern: `(a+)+$`},
			out(strings.Repeat("a", 200000) + "b"),
			false,
		},
	})
}

func TestRegexCompileErrors(t *testing.T) {
	t.Parallel()
	tests := map[string]config.AssertSpec{
		"invalid pattern":     {Kind: "regex", Pattern: "("},
		"invalid via value":   {Kind: "regex", Value: "[a-"},
		"non-string value":    {Kind: "regex", Value: 12},
		"missing pattern":     {Kind: "regex"},
		"invalid ci":          {Kind: "regex", Pattern: "*", CaseInsensitive: true},
		"unknown kind":        {Kind: "nope"},
		"json_path bad path":  {Kind: "json_path", Path: "$[["},
		"json_path bad regex": {Kind: "json_path", Path: "$.a", Regex: "("},
		"max_steps non-int":   {Kind: "max_steps", Value: "many"},
		"max_tool_calls nil":  {Kind: "max_tool_calls"},
		"judge without judge": {Kind: "judge"},
		"not wraps invalid":   {Not: &config.AssertSpec{Kind: "regex", Pattern: "("}},
		"tool_called bad arg": {
			Kind: "tool_called",
			Name: "t",
			Args: map[string]any{"a": map[string]any{"regex": "("}},
		},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, err := assert.Compile(spec, assert.Deps{})
			if !errors.Is(err, domain.ErrConfig) {
				t.Fatalf("err = %v, want ErrConfig", err)
			}
			if a != nil {
				t.Fatal("assertion must be nil on error")
			}
		})
	}
}

func TestJSONValid(t *testing.T) {
	t.Parallel()
	runChecks(t, []check{
		{"object", config.AssertSpec{Kind: "json_valid"}, out(`{"a":1}`), true},
		{"array", config.AssertSpec{Kind: "json_valid"}, out(`[1,2,3]`), true},
		{"trailing prose", config.AssertSpec{Kind: "json_valid"}, out(`{"a":1} hope that helps`), true},
		{"leading prose", config.AssertSpec{Kind: "json_valid"}, out(`Sure! {"a":1}`), true},
		{
			"fenced json block",
			config.AssertSpec{Kind: "json_valid"},
			out("Here:\n```json\n{\"a\": [1,2]}\n```\nDone"),
			true,
		},
		{"fenced block without language", config.AssertSpec{Kind: "json_valid"}, out("```\n[1]\n```"), true},
		{
			"fence with broken json falls back to brace scan",
			config.AssertSpec{Kind: "json_valid"},
			out("```json\nnope\n``` then {\"a\":1}"),
			true,
		},
		{"braces in prose before json", config.AssertSpec{Kind: "json_valid"}, out(`use {x} like {"a":1}`), true},
		{"scalar number rejected", config.AssertSpec{Kind: "json_valid"}, out(`42`), false},
		{"scalar string rejected", config.AssertSpec{Kind: "json_valid"}, out(`"hi"`), false},
		{"null rejected", config.AssertSpec{Kind: "json_valid"}, out(`null`), false},
		{"true rejected", config.AssertSpec{Kind: "json_valid"}, out(`true`), false},
		{"truncated object", config.AssertSpec{Kind: "json_valid"}, out(`{"a":`), false},
		{"empty", config.AssertSpec{Kind: "json_valid"}, out(``), false},
		{"unicode values", config.AssertSpec{Kind: "json_valid"}, out(`{"nome":"João 日本"}`), true},
		{"unterminated fence", config.AssertSpec{Kind: "json_valid"}, out("```json\n{\"a\":1}"), true},
		{"deeply nested unterminated", config.AssertSpec{Kind: "json_valid"}, out(strings.Repeat("[", 2000)), false},
		{"huge array", config.AssertSpec{Kind: "json_valid"}, out("[" + strings.Repeat("1,", 500000) + "1]"), true},
	})
}

func TestExtractJSONUseNumber(t *testing.T) {
	t.Parallel()
	v, err := assert.ExtractJSON(`{"id": 12345678901234567890}`)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("v = %T", v)
	}
	if n, isNum := m["id"].(json.Number); !isNum || n.String() != "12345678901234567890" {
		t.Fatalf("id = %#v", m["id"])
	}
}

func TestJSONSchema(t *testing.T) {
	t.Parallel()
	schema := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"required":             []any{"total", "currency"},
		"additionalProperties": false,
		"properties": map[string]any{
			"total":    map[string]any{"type": "number", "minimum": 0},
			"currency": map[string]any{"enum": []any{"USD", "EUR"}},
			"tags": map[string]any{
				"type":        "array",
				"prefixItems": []any{map[string]any{"type": "string"}},
				"items":       false,
			},
		},
	}
	spec := config.AssertSpec{Kind: "json_schema", Schema: schema}
	runChecks(t, []check{
		{"valid", spec, out(`{"total": 12.5, "currency": "USD"}`), true},
		{"valid in fence", spec, out("```json\n{\"total\": 1, \"currency\": \"EUR\"}\n```"), true},
		{"valid with prefixItems", spec, out(`{"total": 1, "currency": "EUR", "tags": ["a"]}`), true},
		{
			"prefixItems items false rejects second",
			spec,
			out(`{"total": 1, "currency": "EUR", "tags": ["a","b"]}`),
			false,
		},
		{"missing required", spec, out(`{"total": 1}`), false},
		{"wrong type", spec, out(`{"total": "1", "currency": "USD"}`), false},
		{"enum violation", spec, out(`{"total": 1, "currency": "BRL"}`), false},
		{"minimum violation", spec, out(`{"total": -1, "currency": "USD"}`), false},
		{"additional property", spec, out(`{"total": 1, "currency": "USD", "x": 1}`), false},
		{"not json", spec, out(`hello`), false},
		{"big integer stays valid", spec, out(`{"total": 12345678901234567890, "currency": "USD"}`), true},
	})
}

func TestJSONSchemaInvalid(t *testing.T) {
	t.Parallel()
	tests := map[string]any{
		"bad type keyword":  map[string]any{"type": "strng-ish"},
		"bad minimum":       map[string]any{"minimum": "zero"},
		"bad ref":           map[string]any{"$ref": "#/$defs/missing"},
		"unmarshalable":     map[string]any{"x": make(chan int)},
		"required not list": map[string]any{"required": "a"},
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := assert.Compile(config.AssertSpec{Kind: "json_schema", Schema: schema}, assert.Deps{})
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestJSONSchemaInvalidWrapsErrConfig(t *testing.T) {
	t.Parallel()
	_, err := assert.Compile(
		config.AssertSpec{Kind: "json_schema", Schema: map[string]any{"type": "strng-ish"}},
		assert.Deps{},
	)
	if !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("err = %v, want ErrConfig", err)
	}
}

func TestJSONPath(t *testing.T) {
	t.Parallel()
	doc := `{"order":{"total":1499,"currency":"USD","id":"A-100","items":[{"sku":"x","qty":2},{"sku":"y","qty":1}],"note":null}}`
	runChecks(t, []check{
		{
			"equals int vs json int",
			config.AssertSpec{Kind: "json_path", Path: "$.order.total", Equals: 1499},
			out(doc),
			true,
		},
		{
			"equals float 1499.0",
			config.AssertSpec{Kind: "json_path", Path: "$.order.total", Equals: 1499.0},
			out(doc),
			true,
		},
		{
			"equals numeric string against number",
			config.AssertSpec{Kind: "json_path", Path: "$.order.total", Equals: "1499"},
			out(doc),
			true,
		},
		{
			"equals number against string leaf",
			config.AssertSpec{Kind: "json_path", Path: "$.order.id", Equals: 1499},
			out(doc),
			false,
		},
		{"equals differs", config.AssertSpec{Kind: "json_path", Path: "$.order.total", Equals: 1500}, out(doc), false},
		{
			"equals string",
			config.AssertSpec{Kind: "json_path", Path: "$.order.currency", Equals: "USD"},
			out(doc),
			true,
		},
		{
			"string leaf numeric text not coerced",
			config.AssertSpec{Kind: "json_path", Path: "$.order.currency", Equals: 0},
			out(doc),
			false,
		},
		{
			"wildcard any element equals",
			config.AssertSpec{Kind: "json_path", Path: "$.order.items[*].sku", Equals: "y"},
			out(doc),
			true,
		},
		{
			"wildcard no element equals",
			config.AssertSpec{Kind: "json_path", Path: "$.order.items[*].sku", Equals: "z"},
			out(doc),
			false,
		},
		{"regex match", config.AssertSpec{Kind: "json_path", Path: "$.order.id", Regex: `^[A-Z]-\d+$`}, out(doc), true},
		{"regex mismatch", config.AssertSpec{Kind: "json_path", Path: "$.order.id", Regex: `^\d+$`}, out(doc), false},
		{
			"regex on non-string",
			config.AssertSpec{Kind: "json_path", Path: "$.order.total", Regex: `1499`},
			out(doc),
			false,
		},
		{"exists default true found", config.AssertSpec{Kind: "json_path", Path: "$.order.currency"}, out(doc), true},
		{
			"exists true not found",
			config.AssertSpec{Kind: "json_path", Path: "$.order.nothing", Exists: new(true)},
			out(doc),
			false,
		},
		{
			"exists false absent",
			config.AssertSpec{Kind: "json_path", Path: "$.order.nothing", Exists: new(false)},
			out(doc),
			true,
		},
		{
			"exists false present",
			config.AssertSpec{Kind: "json_path", Path: "$.order.total", Exists: new(false)},
			out(doc),
			false,
		},
		{"not found with equals", config.AssertSpec{Kind: "json_path", Path: "$.missing", Equals: 1}, out(doc), false},
		{"explicit null exists", config.AssertSpec{Kind: "json_path", Path: "$.order.note"}, out(doc), true},
		{"not json output", config.AssertSpec{Kind: "json_path", Path: "$.a", Equals: 1}, out("plain"), false},
		{
			"json in fence",
			config.AssertSpec{Kind: "json_path", Path: "$.a", Equals: 1},
			out("```json\n{\"a\":1}\n```"),
			true,
		},
		{"array root", config.AssertSpec{Kind: "json_path", Path: "$[1]", Equals: 2}, out("[1,2,3]"), true},
		{"big int exact path", config.AssertSpec{Kind: "json_path", Path: "$.a", Equals: 7}, out(`{"a":7.0}`), true},
	})
}

func TestNot(t *testing.T) {
	t.Parallel()
	inner := config.AssertSpec{Kind: "contains", Value: "x"}
	tests := []struct {
		name string
		in   string
		pass bool
	}{
		{"inner passes so not fails", "x", false},
		{"inner fails so not passes", "y", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, err := assert.Compile(config.AssertSpec{Not: &inner}, assert.Deps{})
			if err != nil {
				t.Fatal(err)
			}
			if a.Kind() != "not contains" {
				t.Fatalf("kind = %q", a.Kind())
			}
			r, err := a.Evaluate(context.Background(), out(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if r.Pass != tt.pass || r.Kind != "not contains" {
				t.Fatalf("result = %+v", r)
			}
			if r.Pass && r.Reason != "" || !r.Pass && r.Reason == "" {
				t.Fatalf("reason = %q for pass=%v", r.Reason, r.Pass)
			}
		})
	}
}

func TestNotNested(t *testing.T) {
	t.Parallel()
	spec := config.AssertSpec{Not: &config.AssertSpec{Not: &config.AssertSpec{Kind: "contains", Value: "x"}}}
	a, err := assert.Compile(spec, assert.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Evaluate(context.Background(), out("x"))
	if err != nil || !r.Pass {
		t.Fatalf("double negation should pass: %+v %v", r, err)
	}
}

type fakeJudge struct {
	verdict domain.JudgeVerdict
	err     error
	got     assert.JudgeRequest
}

func (f *fakeJudge) Judge(_ context.Context, req assert.JudgeRequest) (domain.JudgeVerdict, error) {
	f.got = req
	return f.verdict, f.err
}

func TestNotInvertsScoreAndPropagatesError(t *testing.T) {
	t.Parallel()
	fj := &fakeJudge{verdict: domain.JudgeVerdict{Pass: true, Score: 0.75}}
	a, err := assert.Compile(
		config.AssertSpec{Not: &config.AssertSpec{Kind: "judge", Rubric: []config.Criterion{{ID: "a", Pass: "p"}}}},
		assert.Deps{Judge: fj},
	)
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Evaluate(context.Background(), out("o"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass || r.Score == nil || *r.Score != 0.25 {
		t.Fatalf("result = %+v", r)
	}

	boom := errors.New("boom")
	fj.err = boom
	if _, err := a.Evaluate(context.Background(), out("o")); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestCostMax(t *testing.T) {
	t.Parallel()
	runChecks(t, []check{
		{"nil cost fails", config.AssertSpec{Kind: "cost_max", MaxUSD: 1}, assert.Input{}, false},
		{"under", config.AssertSpec{Kind: "cost_max", MaxUSD: 0.01}, assert.Input{CostUSD: new(0.005)}, true},
		{
			"equal limit passes",
			config.AssertSpec{Kind: "cost_max", MaxUSD: 0.01},
			assert.Input{CostUSD: new(0.01)},
			true,
		},
		{"over", config.AssertSpec{Kind: "cost_max", MaxUSD: 0.01}, assert.Input{CostUSD: new(0.0100001)}, false},
		{"zero cost under zero limit", config.AssertSpec{Kind: "cost_max"}, assert.Input{CostUSD: new(0.0)}, true},
	})
}

func TestLatencyMax(t *testing.T) {
	t.Parallel()
	runChecks(t, []check{
		{
			"under",
			config.AssertSpec{Kind: "latency_max", MaxMS: 500},
			assert.Input{Latency: 499 * time.Millisecond},
			true,
		},
		{
			"equal",
			config.AssertSpec{Kind: "latency_max", MaxMS: 500},
			assert.Input{Latency: 500 * time.Millisecond},
			true,
		},
		{
			"over",
			config.AssertSpec{Kind: "latency_max", MaxMS: 500},
			assert.Input{Latency: 501 * time.Millisecond},
			false,
		},
		{"zero latency", config.AssertSpec{Kind: "latency_max", MaxMS: 1}, assert.Input{}, true},
	})
}

func TestNotRefused(t *testing.T) {
	t.Parallel()
	refusalStep := domain.Step{Response: domain.Response{StopReason: domain.StopRefusal}}
	okStep := domain.Step{Response: domain.Response{StopReason: domain.StopEnd}}
	runChecks(t, []check{
		{"plain output", config.AssertSpec{Kind: "not_refused"}, out("fine"), true},
		{
			"final refusal",
			config.AssertSpec{Kind: "not_refused"},
			assert.Input{Final: domain.Response{StopReason: domain.StopRefusal}},
			false,
		},
		{
			"refusal in trajectory step",
			config.AssertSpec{Kind: "not_refused"},
			assert.Input{Trajectory: domain.Trajectory{Steps: []domain.Step{okStep, refusalStep}}},
			false,
		},
		{
			"no refusal in trajectory",
			config.AssertSpec{Kind: "not_refused"},
			assert.Input{Trajectory: domain.Trajectory{Steps: []domain.Step{okStep}}},
			true,
		},
	})
}

func TestToolCalled(t *testing.T) {
	t.Parallel()
	refund := toolStep(tc("issue_refund", `{"order_id":"A-100","reason":"damaged"}`))
	lookup := toolStep(tc("lookup_order", `{"order_id":"A-100"}`))
	twice := traj(lookup, refund, refund)
	runChecks(t, []check{
		{"called once default min", config.AssertSpec{Kind: "tool_called", Name: "lookup_order"}, traj(lookup), true},
		{"never called", config.AssertSpec{Kind: "tool_called", Name: "issue_refund"}, traj(lookup), false},
		{"empty trajectory", config.AssertSpec{Kind: "tool_called", Name: "x"}, traj(), false},
		{
			"args subset match",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Args: map[string]any{"order_id": "A-100"}},
			traj(refund),
			true,
		},
		{
			"args value mismatch",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Args: map[string]any{"order_id": "B-1"}},
			traj(refund),
			false,
		},
		{
			"args regex",
			config.AssertSpec{
				Kind: "tool_called",
				Name: "issue_refund",
				Args: map[string]any{"reason": map[string]any{"regex": "^dam"}},
			},
			traj(refund),
			true,
		},
		{
			"args regex mismatch",
			config.AssertSpec{
				Kind: "tool_called",
				Name: "issue_refund",
				Args: map[string]any{"reason": map[string]any{"regex": "^zzz"}},
			},
			traj(refund),
			false,
		},
		{
			"args with unparsable call arguments",
			config.AssertSpec{Kind: "tool_called", Name: "t", Args: map[string]any{"a": 1}},
			traj(toolStep(tc("t", `not json`))),
			false,
		},
		{
			"times min satisfied",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Times: config.Times{Min: new(2)}},
			twice,
			true,
		},
		{
			"times min unmet",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Times: config.Times{Min: new(3)}},
			twice,
			false,
		},
		{
			"times max exceeded",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Times: config.Times{Max: new(1)}},
			twice,
			false,
		},
		{
			"times max ok",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Times: config.Times{Min: new(1), Max: new(2)}},
			twice,
			true,
		},
		{
			"times min zero max zero with no calls",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Times: config.Times{Min: new(0), Max: new(0)}},
			traj(lookup),
			true,
		},
		{
			"times max zero with call",
			config.AssertSpec{Kind: "tool_called", Name: "issue_refund", Times: config.Times{Min: new(0), Max: new(0)}},
			traj(refund),
			false,
		},
		{
			"parallel calls in one step counted",
			config.AssertSpec{Kind: "tool_called", Name: "a", Times: config.Times{Min: new(2)}},
			traj(toolStep(tc("a", `{}`), tc("a", `{}`))),
			true,
		},
	})
}

func TestToolNotCalled(t *testing.T) {
	t.Parallel()
	refund := toolStep(tc("issue_refund", `{"order_id":"A-100"}`))
	runChecks(t, []check{
		{"absent", config.AssertSpec{Kind: "tool_not_called", Name: "issue_refund"}, traj(), true},
		{"present", config.AssertSpec{Kind: "tool_not_called", Name: "issue_refund"}, traj(refund), false},
		{
			"present with other args passes",
			config.AssertSpec{Kind: "tool_not_called", Name: "issue_refund", Args: map[string]any{"order_id": "Z-1"}},
			traj(refund),
			true,
		},
		{
			"present with matching args fails",
			config.AssertSpec{Kind: "tool_not_called", Name: "issue_refund", Args: map[string]any{"order_id": "A-100"}},
			traj(refund),
			false,
		},
		{
			"times ignored for not_called",
			config.AssertSpec{Kind: "tool_not_called", Name: "issue_refund", Times: config.Times{Min: new(5)}},
			traj(refund),
			false,
		},
	})
}

func TestToolOrder(t *testing.T) {
	t.Parallel()
	a, b, c := toolStep(tc("a", `{}`)), toolStep(tc("b", `{}`)), toolStep(tc("c", `{}`))
	runChecks(t, []check{
		{
			"subsequence with gaps",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "c"}},
			traj(a, b, c),
			true,
		},
		{
			"subsequence wrong order",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"c", "a"}},
			traj(a, b, c),
			false,
		},
		{
			"subsequence missing",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "d"}},
			traj(a, b, c),
			false,
		},
		{
			"subsequence with repeats",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "a"}},
			traj(a, b, a),
			true,
		},
		{
			"subsequence needs two",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "a"}},
			traj(a, b),
			false,
		},
		{"empty sequence passes", config.AssertSpec{Kind: "tool_order"}, traj(a), true},
		{
			"strict exact",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "b"}, Strict: true},
			traj(a, b),
			true,
		},
		{
			"strict extra call",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "b"}, Strict: true},
			traj(a, b, c),
			false,
		},
		{
			"strict missing call",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "b"}, Strict: true},
			traj(a),
			false,
		},
		{"strict empty both", config.AssertSpec{Kind: "tool_order", Strict: true}, traj(), true},
		{
			"parallel calls flattened in order",
			config.AssertSpec{Kind: "tool_order", Sequence: []string{"a", "b"}, Strict: true},
			traj(toolStep(tc("a", `{}`), tc("b", `{}`))),
			true,
		},
	})
}

func TestMaxCounts(t *testing.T) {
	t.Parallel()
	three := traj(toolStep(tc("a", `{}`), tc("a", `{}`)), toolStep(tc("b", `{}`)), domain.Step{})
	runChecks(t, []check{
		{"steps within", config.AssertSpec{Kind: "max_steps", Value: 3}, three, true},
		{"steps exceeded", config.AssertSpec{Kind: "max_steps", Value: 2}, three, false},
		{"steps float value", config.AssertSpec{Kind: "max_steps", Value: 3.0}, three, true},
		{"tool calls within", config.AssertSpec{Kind: "max_tool_calls", Value: 3}, three, true},
		{"tool calls exceeded", config.AssertSpec{Kind: "max_tool_calls", Value: 2}, three, false},
		{"tool calls zero with none", config.AssertSpec{Kind: "max_tool_calls", Value: 0}, traj(), true},
	})
}

func TestJudge(t *testing.T) {
	t.Parallel()
	cost := 0.002
	fj := &fakeJudge{verdict: domain.JudgeVerdict{
		Pass: true, Score: 0.5, Reason: "because", Usage: domain.Usage{InputTokens: 10, Known: true}, CostUSD: &cost,
	}}
	spec := config.AssertSpec{
		Kind:    "judge",
		Target:  "j1",
		Samples: 0,
		Rubric:  []config.Criterion{{ID: "c1", Pass: "is polite"}},
	}
	a, err := assert.Compile(spec, assert.Deps{Judge: fj, JudgeSamples: 5})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Evaluate(context.Background(), assert.Input{CaseID: "case", Output: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	wantReq := assert.JudgeRequest{
		CaseID: "case", Output: "hello", Target: "j1", Samples: 5,
		Rubric: []assert.Criterion{{ID: "c1", Pass: "is polite"}},
	}
	if diff := cmp.Diff(wantReq, fj.got); diff != "" {
		t.Fatalf("request mismatch (-want +got):\n%s", diff)
	}
	if !r.Pass || r.Kind != "judge" || r.Score == nil || *r.Score != 0.5 || r.Reason != "because" ||
		r.Usage.InputTokens != 10 || r.CostUSD == nil || *r.CostUSD != cost {
		t.Fatalf("result = %+v", r)
	}

	spec.Samples = 3
	a, err = assert.Compile(spec, assert.Deps{Judge: fj, JudgeSamples: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Evaluate(context.Background(), out("x")); err != nil {
		t.Fatal(err)
	}
	if fj.got.Samples != 3 {
		t.Fatalf("spec samples should override deps, got %d", fj.got.Samples)
	}
}

func TestJudgeFailureAndError(t *testing.T) {
	t.Parallel()
	fj := &fakeJudge{verdict: domain.JudgeVerdict{Pass: false, Score: 0, Reason: "nope"}}
	a, err := assert.Compile(config.AssertSpec{Kind: "judge"}, assert.Deps{Judge: fj})
	if err != nil {
		t.Fatal(err)
	}
	r, err := a.Evaluate(context.Background(), out("x"))
	if err != nil || r.Pass || r.Reason != "nope" {
		t.Fatalf("result = %+v err=%v", r, err)
	}
	fj.err = domain.ErrAuth
	if _, err := a.Evaluate(context.Background(), out("x")); !errors.Is(err, domain.ErrAuth) {
		t.Fatalf("err = %v", err)
	}
}

func TestKinds(t *testing.T) {
	t.Parallel()
	for _, k := range []string{
		"equals", "contains", "regex", "json_valid", "json_schema", "json_path", "cost_max", "latency_max",
		"not_refused", "tool_called", "tool_not_called", "tool_order", "max_steps", "max_tool_calls",
	} {
		t.Run(k, func(t *testing.T) {
			t.Parallel()
			spec := config.AssertSpec{Kind: k, Value: 1, Path: "$.a", Schema: map[string]any{}, Pattern: "x"}
			if k == "equals" || k == "contains" {
				spec.Value = "x"
			}
			a, err := assert.Compile(spec, assert.Deps{})
			if err != nil {
				t.Fatal(err)
			}
			if a.Kind() != k {
				t.Fatalf("Kind = %q", a.Kind())
			}
		})
	}
}

func FuzzExtractJSON(f *testing.F) {
	for _, s := range []string{
		`{"a":1}`, "```json\n[1]\n```", "prose {x} {\"a\":[1,2]} tail", `[`, `{`, "```", "", "42", `"s"`,
		"```json\n{\"a\":1}", "{{{{", "[[[[]]]]",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := assert.ExtractJSON(s)
		if err != nil {
			if v != nil {
				t.Fatalf("value %v returned with error", v)
			}
			return
		}
		switch v.(type) {
		case map[string]any, []any:
		default:
			t.Fatalf("scalar %T returned", v)
		}
		if _, err := json.Marshal(v); err != nil {
			t.Fatalf("result not re-encodable: %v", err)
		}
	})
}

func FuzzCompileEvaluate(f *testing.F) {
	f.Add("regex", "a+", "aaa")
	f.Add("contains", "x", "xyz")
	f.Add("json_path", "$.a", `{"a":1}`)
	f.Add("equals", "x", " x ")
	f.Fuzz(func(t *testing.T, kind, arg, output string) {
		spec := config.AssertSpec{Kind: kind, Pattern: arg, Path: arg, Value: arg, Regex: arg}
		a, err := assert.Compile(spec, assert.Deps{})
		if err != nil {
			return
		}
		if _, err := a.Evaluate(context.Background(), out(output)); err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
	})
}

func TestExtractJSONHostileInputIsLinear(t *testing.T) {
	t.Parallel()
	start := time.Now()
	if _, err := assert.ExtractJSON(strings.Repeat("[", 50000)); err == nil {
		t.Fatal("unbalanced brackets must not decode")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("ExtractJSON took %s on 50k brackets", d)
	}
}

package config

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

const baseYAML = `version: 1
suite: demo
providers:
  mock:
    kind: mock
  oa:
    kind: openai
    api_key_env: OPENAI_API_KEY
  an:
    kind: anthropic
    api_key_env: ANTHROPIC_API_KEY
targets:
  fast:
    provider: mock
    model: m1
  gpt-mini:
    provider: oa
    model: gpt-5-mini
  claude:
    provider: an
    model: claude-haiku-5-5
tests:
  - id: t1
    prompt: hello
    assert:
      - kind: contains
        value: hi
  - id: t2
    prompt: bye
    assert:
      - kind: contains
        value: bye
`

func noEnv(string) (string, bool) { return "", false }

func mapEnv(m map[string]string) LookupEnv {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func lineOf(t *testing.T, doc, needle string, last bool) int {
	t.Helper()
	found := 0
	for i, l := range strings.Split(doc, "\n") {
		if strings.Contains(l, needle) {
			found = i + 1
			if !last {
				return found
			}
		}
	}
	if found == 0 {
		t.Fatalf("needle %q not in document", needle)
	}
	return found
}

func parseErr(t *testing.T, doc string) *ValidationError {
	t.Helper()
	_, err := Parse("suite.yaml", []byte(doc), noEnv)
	if err == nil {
		t.Fatal("expected error")
	}
	ve, ok := AsValidation(err)
	if !ok {
		t.Fatalf("want *ValidationError, got %T: %v", err, err)
	}
	if !errors.Is(err, domain.ErrConfig) {
		t.Fatalf("validation error must match domain.ErrConfig")
	}
	return ve
}

func TestParseValidationErrorLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		doc      string
		needle   string
		last     bool
		wantPath string
		wantMsg  string
	}{
		{
			name:    "unknown field",
			doc:     strings.Replace(baseYAML, "suite: demo\n", "suite: demo\nbogus: 1\n", 1),
			needle:  "bogus",
			wantMsg: "bogus",
		},
		{
			name: "unknown assertion kind",
			doc: strings.Replace(
				baseYAML,
				"kind: contains\n        value: hi",
				"kind: nope\n        value: hi",
				1,
			),
			needle:   "kind: nope",
			wantPath: "$.tests[0].assert[0].kind",
			wantMsg:  "unknown assertion kind",
		},
		{
			name: "anthropic temperature",
			doc: strings.Replace(baseYAML, "    model: claude-haiku-5-5\n",
				"    model: claude-haiku-5-5\n    params:\n      temperature: 0.2\n", 1),
			needle:   "temperature",
			wantPath: "$.targets.claude.params.temperature",
			wantMsg:  "rejects temperature",
		},
		{
			name: "inline api key",
			doc: strings.Replace(baseYAML, "    api_key_env: OPENAI_API_KEY\n",
				"    api_key_env: OPENAI_API_KEY\n    api_key: sk-secret\n", 1),
			needle:   "api_key: sk",
			wantPath: "$.providers.oa.api_key",
			wantMsg:  "inline API keys",
		},
		{
			name:     "unknown provider on quoted-key target",
			doc:      strings.Replace(baseYAML, "provider: oa\n", "provider: ghost\n", 1),
			needle:   "provider: ghost",
			wantPath: "$.targets.'gpt-mini'.provider",
			wantMsg:  `unknown provider "ghost"`,
		},
		{
			name:     "duplicate test id",
			doc:      strings.Replace(baseYAML, "id: t2", "id: t1", 1),
			needle:   "id: t1",
			last:     true,
			wantPath: "$.tests[1].id",
			wantMsg:  "duplicate test id",
		},
		{
			name: "missing judge target",
			doc: "defaults:\n  samples: 2\n" + strings.Replace(baseYAML, "kind: contains\n        value: hi",
				"kind: judge\n        rubric:\n          - id: a\n            pass: ok", 1),
			needle:   "samples: 2",
			wantPath: "$.defaults.judge",
			wantMsg:  "judge assertions need defaults.judge.target",
		},
		{
			name: "unknown judge target",
			doc: "defaults:\n  judge:\n    target: ghost\n" + strings.Replace(
				baseYAML,
				"kind: contains\n        value: hi",
				"kind: judge\n        rubric:\n          - id: a\n            pass: ok",
				1,
			),
			needle:   "target: ghost",
			wantPath: "$.defaults.judge.target",
			wantMsg:  `unknown judge target "ghost"`,
		},
		{
			name: "compare target outside matrix",
			doc: baseYAML + `compare:
  - base: fast
    head: ghost
`,
			needle:   "- base: fast",
			wantPath: "$.compare[0]",
			wantMsg:  `target "ghost" is not in the matrix`,
		},
		{
			name: "agent assertion without agent block",
			doc: strings.Replace(
				baseYAML,
				"kind: contains\n        value: hi",
				"kind: tool_called\n        name: x",
				1,
			),
			needle:   "kind: tool_called",
			wantPath: "$.tests[0].assert[0]",
			wantMsg:  "needs an agent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ve := parseErr(t, tc.doc)
			want := lineOf(t, tc.doc, tc.needle, tc.last)
			var got *FieldError
			for i := range ve.Fields {
				f := &ve.Fields[i]
				if f.Line == want && strings.Contains(f.Msg, tc.wantMsg) &&
					(tc.wantPath == "" || f.Path == tc.wantPath) {
					got = f
				}
			}
			if got == nil {
				t.Fatalf("no field error at line %d matching %q path %q; got %v", want, tc.wantMsg, tc.wantPath, ve)
			}
			if got.File != "suite.yaml" {
				t.Fatalf("file = %q", got.File)
			}
			if !strings.HasPrefix(got.Error(), "suite.yaml:"+strconv.Itoa(want)+": ") {
				t.Fatalf("error format %q", got.Error())
			}
		})
	}
}

func TestFieldErrorFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   FieldError
		want string
	}{
		{"all parts", FieldError{File: "a.yaml", Line: 4, Path: "$.x.y", Msg: "bad"}, "a.yaml:4: x.y: bad"},
		{"no line", FieldError{File: "a.yaml", Path: "$.x", Msg: "bad"}, "a.yaml: x: bad"},
		{"no path", FieldError{File: "a.yaml", Line: 2, Msg: "bad"}, "a.yaml:2: bad"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.in.Error(); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	ve := &ValidationError{Fields: []FieldError{{File: "f", Msg: "a"}, {File: "f", Msg: "b"}}}
	if got := ve.Error(); got != "f: a\nf: b" {
		t.Fatalf("got %q", got)
	}
}

func TestAsValidationRejectsOtherErrors(t *testing.T) {
	t.Parallel()
	if ve, ok := AsValidation(errors.New("x")); ok || ve != nil {
		t.Fatal("plain error must not be a validation error")
	}
}

func TestParseValidationRules(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		doc     string
		wantMsg string
	}{
		{"bad version", strings.Replace(baseYAML, "version: 1", "version: 2", 1), "unsupported version"},
		{"bad suite name", strings.Replace(baseYAML, "suite: demo", "suite: 'a b'", 1), "suite name"},
		{"unknown kind", strings.Replace(baseYAML, "kind: mock", "kind: weird", 1), "unknown provider kind"},
		{
			"openai missing key env",
			strings.Replace(baseYAML, "    api_key_env: OPENAI_API_KEY\n", "", 1),
			"api_key_env is required",
		},
		{
			"compat missing base url",
			strings.Replace(baseYAML, "kind: mock", "kind: openai_compatible", 1),
			"base_url is required",
		},
		{
			"negative limits",
			strings.Replace(baseYAML, "kind: mock", "kind: mock\n    limits:\n      rpm: -1", 1),
			"limits must be non-negative",
		},
		{"missing model", strings.Replace(baseYAML, "    model: m1\n", "", 1), "model is required"},
		{
			"anthropic seed",
			strings.Replace(
				baseYAML,
				"    model: claude-haiku-5-5\n",
				"    model: claude-haiku-5-5\n    params:\n      seed: 1\n",
				1,
			),
			"seed is not supported",
		},
		{
			"effort on openai",
			strings.Replace(
				baseYAML,
				"    model: gpt-5-mini\n",
				"    model: gpt-5-mini\n    params:\n      anthropic:\n        effort: high\n",
				1,
			),
			"anthropic params on",
		},
		{
			"unknown effort",
			strings.Replace(
				baseYAML,
				"    model: claude-haiku-5-5\n",
				"    model: claude-haiku-5-5\n    params:\n      anthropic:\n        effort: turbo\n",
				1,
			),
			"unknown effort",
		},
		{
			"unknown reasoning effort",
			strings.Replace(
				baseYAML,
				"    model: gpt-5-mini\n",
				"    model: gpt-5-mini\n    params:\n      openai:\n        reasoning_effort: turbo\n",
				1,
			),
			"unknown reasoning_effort",
		},
		{
			"openai params on anthropic",
			strings.Replace(
				baseYAML,
				"    model: claude-haiku-5-5\n",
				"    model: claude-haiku-5-5\n    params:\n      openai:\n        reasoning_effort: low\n",
				1,
			),
			"openai params on an anthropic",
		},
		{"invalid target name", strings.Replace(baseYAML, "  fast:", "  'bad name':", 1), "target name"},
		{"test without id", strings.Replace(baseYAML, "  - id: t1\n", "  - id: ''\n", 1), "id is required"},
		{"hash in id", strings.Replace(baseYAML, "id: t1", "id: 't#1'", 1), "must not contain"},
		{
			"prompt and turns",
			strings.Replace(baseYAML, "    prompt: hello\n", "    prompt: hello\n    turns:\n      - user: x\n", 1),
			"exactly one of prompt or turns",
		},
		{
			"neither prompt nor turns",
			strings.Replace(baseYAML, "    prompt: hello\n", "", 1),
			"exactly one of prompt or turns",
		},
		{
			"negative samples",
			strings.Replace(baseYAML, "    prompt: hello\n", "    prompt: hello\n    samples: -1\n", 1),
			"samples must be non-negative",
		},
		{
			"unknown agent tool",
			strings.Replace(
				baseYAML,
				"    prompt: hello\n",
				"    prompt: hello\n    agent:\n      tools: [ghost]\n",
				1,
			),
			"unknown tool",
		},
		{
			"no assertions",
			strings.Replace(baseYAML, "    assert:\n      - kind: contains\n        value: hi\n", "", 1),
			"at least one assertion",
		},
		{
			"equals without value",
			strings.Replace(baseYAML, "kind: contains\n        value: hi", "kind: equals", 1),
			"equals requires value",
		},
		{
			"cost_max without max_usd",
			strings.Replace(baseYAML, "kind: contains\n        value: hi", "kind: cost_max", 1),
			"cost_max requires max_usd",
		},
		{
			"max_steps non integer",
			strings.Replace(baseYAML, "kind: contains\n        value: hi", "kind: max_steps\n        value: 1.5", 1),
			"max_steps requires",
		},
		{
			"not combined with kind",
			strings.Replace(
				baseYAML,
				"kind: contains\n        value: hi",
				"kind: contains\n        not:\n          kind: contains\n          value: x",
				1,
			),
			"cannot be combined",
		},
		{
			"empty rubric criterion",
			strings.Replace(
				baseYAML,
				"kind: contains\n        value: hi",
				"kind: judge\n        target: fast\n        rubric:\n          - id: a",
				1,
			),
			"criterion needs id and pass",
		},
		{"tool mock count", baseYAML + "tools:\n  x:\n    description: d\n", "exactly one of static"},
		{
			"tool bad parameters",
			baseYAML + "tools:\n  x:\n    parameters: [1]\n    mock:\n      static: 1\n",
			"JSON Schema object",
		},
		{
			"tool negative latency",
			baseYAML + "tools:\n  x:\n    mock:\n      static: 1\n      latency_ms: -1\n",
			"latency_ms must be non-negative",
		},
		{"unknown matrix target", baseYAML + "matrix:\n  targets: [ghost]\n", `unknown target "ghost"`},
		{"compare same", baseYAML + "compare:\n  - base: fast\n    head: fast\n", "must differ"},
		{
			"compare bad mode",
			baseYAML + "compare:\n  - base: fast\n    head: claude\n    mode: weird\n",
			"unknown mode",
		},
		{"alpha too high", baseYAML + "gates:\n  alpha: 1.5\n", "alpha must be in"},
		{"min effect too high", baseYAML + "gates:\n  min_effect: 1\n", "min_effect must be in"},
		{"min pass rate", baseYAML + "gates:\n  min_pass_rate: 2\n", "min_pass_rate must be in"},
		{"max error rate", baseYAML + "gates:\n  max_error_rate: -0.1\n", "max_error_rate must be in"},
		{
			"missing prompt file",
			strings.Replace(baseYAML, "prompt: hello", "prompt: hello\n    vars:\n      a: file://nope.txt", 1),
			"read file://nope.txt",
		},
		{"invalid yaml", "version: [", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ve := parseErr(t, tc.doc)
			if !strings.Contains(ve.Error(), tc.wantMsg) {
				t.Fatalf("want message containing %q, got %v", tc.wantMsg, ve)
			}
		})
	}
}

func TestParseMinimalDefaults(t *testing.T) {
	t.Parallel()
	s, err := Parse("suite.yaml", []byte(baseYAML), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if s.Defaults.Samples != 3 || s.Defaults.MaxTokens != 1024 || s.Defaults.Timeout.Seconds() != 60 {
		t.Fatalf("defaults = %+v", s.Defaults)
	}
	if s.Gates.Alpha != 0.05 || s.Gates.MinEffect != 0.03 || s.Defaults.Judge.Samples != 1 {
		t.Fatalf("gates = %+v judge = %+v", s.Gates, s.Defaults.Judge)
	}
	if s.Targets["fast"].Params.MaxTokens != 1024 {
		t.Fatalf("target max tokens = %d", s.Targets["fast"].Params.MaxTokens)
	}
	if diff := cmp.Diff([]string{"claude", "fast", "gpt-mini"}, s.MatrixTargets()); diff != "" {
		t.Fatalf("matrix (-want +got):\n%s", diff)
	}
	if len(s.Cases) != 2 || s.Cases[0].Samples != 3 || s.Cases[0].ID != "t1" || s.Cases[1].TestIdx != 1 {
		t.Fatalf("cases = %+v", s.Cases)
	}
	if got, want := s.Cases[0].Line, lineOf(t, baseYAML, "- id: t1", false); got != want {
		t.Fatalf("case line = %d want %d", got, want)
	}
}

func TestLoad(t *testing.T) {
	t.Parallel()
	dir := writeFiles(t, map[string]string{"s.yaml": baseYAML})
	s, err := Load(filepath.Join(dir, "s.yaml"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if s.Dir != dir || s.Name != "demo" {
		t.Fatalf("dir=%q name=%q", s.Dir, s.Name)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml"), noEnv); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

const fullYAML = `version: 1
suite: full
description: everything
defaults:
  samples: 2
  timeout: 30s
  max_tokens: 256
  judge:
    target: judge
    samples: 3
providers:
  mock:
    kind: mock
    base_url: ${MOCK_URL:-http://mock.local}
    limits: { rpm: 10, tpm: 100, concurrency: 2 }
  oa:
    kind: openai
    api_key_env: OPENAI_API_KEY
  an:
    kind: anthropic
    api_key_env: ANTHROPIC_API_KEY
  local:
    kind: openai_compatible
    base_url: http://localhost:1/v1
targets:
  fast:
    provider: mock
    model: ${FAST_MODEL:-m1}
  judge:
    provider: mock
    model: j1
  gpt-mini:
    provider: oa
    model: gpt-5-mini
    params:
      seed: 7
      temperature: 0.5
      max_tokens: 99
      openai: { reasoning_effort: low }
  claude:
    provider: an
    model: claude-haiku-5-5
    params:
      anthropic: { effort: high }
  llama:
    provider: local
    model: llama
prompts:
  greet: file://prompts/greet.txt
  inline: "Say {{.name}}"
tools:
  lookup:
    description: look up
    parameters: file://schemas/lookup.json
    mock:
      match:
        - when: { id: 1 }
          return: { ok: true }
      default: { ok: false }
  search:
    parameters:
      type: object
    mock:
      static: result
tests:
  - id: single
    prompt: greet
    system: "You are {{.role}}"
    vars:
      name: Ada
      role: tester
      blob: file://prompts/blob.txt
    samples: 5
    assert:
      - kind: equals
        value: "hi {{.name}}"
      - kind: contains
        value: Ada
        any: true
        case_insensitive: true
      - kind: regex
        pattern: "^hi {{.name}}$"
      - kind: json_valid
      - kind: json_schema
        schema: file://schemas/lookup.json
      - kind: json_path
        path: $.a
        equals: "{{.name}}"
        regex: "x{{.name}}"
        exists: true
      - kind: not
        not:
          kind: contains
          value: "bad {{.name}}"
      - not:
          kind: regex
          pattern: zzz
      - kind: cost_max
        max_usd: 0.01
      - kind: latency_max
        max_ms: 1000
      - kind: not_refused
  - id: multi
    turns:
      - user: "first {{.q}}"
      - user: inline
    vars:
      q: why
      name: Bob
    agent:
      tools: [lookup, search]
    assert:
      - kind: tool_called
        name: lookup
        args: { id: "{{.q}}" }
        times: { min: 1, max: 2 }
      - kind: tool_not_called
        name: search
      - kind: tool_order
        sequence: [lookup, search]
        strict: true
      - kind: max_steps
        value: 4
      - kind: max_tool_calls
        value: 6
      - kind: judge
        samples: 2
        weight: 2
        rubric:
          - id: polite
            pass: "mentions {{.q}}"
  - id: rows
    prompt: "Q {{.question}} / {{.expected}}"
    each: data/rows.jsonl
    assert:
      - kind: equals
        value: "{{.expected}}"
  - id: docs
    prompt: "D {{.topic}}"
    each: data/docs/*.json
    assert:
      - kind: contains
        value: "{{.expected}}"
compare:
  - base: fast
    head: gpt-mini
  - base: fast
    head: claude
    mode: noninferiority
    margin: 0.1
gates:
  baseline: .evalvet/base.json
  alpha: 0.01
  min_effect: 0.05
  min_pass_rate: 0.9
  max_error_rate: 0.1
  cost:
    max_increase_pct: 20
`

var fullFiles = map[string]string{
	"prompts/greet.txt":   "hi {{.name}}",
	"prompts/blob.txt":    "BLOB",
	"schemas/lookup.json": `{"type":"object","properties":{"id":{"type":"integer"}}}`,
	"data/rows.jsonl": `{"id":"a","question":"one","expected":"1"}

{"question":"two","expected":"2"}
{"id":"c","vars":{"question":"three"},"expected":"3"}
`,
	"data/docs/x.json": `{"topic":"alpha","expected":"A"}`,
	"data/docs/y.json": `[{"id":"y1","topic":"beta","expected":"B"},{"topic":"gamma","expected":"G"}]`,
}

func parseFull(t *testing.T, env LookupEnv) *Suite {
	t.Helper()
	files := map[string]string{"suite.yaml": fullYAML}
	maps.Copy(files, fullFiles)
	dir := writeFiles(t, files)
	s, err := Load(filepath.Join(dir, "suite.yaml"), env)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestParseFullSuite(t *testing.T) {
	t.Parallel()
	s := parseFull(t, noEnv)

	if s.Defaults.Samples != 2 || s.Defaults.MaxTokens != 256 || s.Defaults.Judge.Samples != 3 {
		t.Fatalf("defaults = %+v", s.Defaults)
	}
	if s.Defaults.Timeout.Seconds() != 30 {
		t.Fatalf("timeout = %v", s.Defaults.Timeout)
	}
	if got := s.Providers["mock"].Limits; got != (Limits{RPM: 10, TPM: 100, Concurrency: 2}) {
		t.Fatalf("limits = %+v", got)
	}
	if got := s.Targets["gpt-mini"].Params; got.MaxTokens != 99 || *got.Seed != 7 || *got.Temperature != 0.5 ||
		got.OpenAI.ReasoningEffort != "low" {
		t.Fatalf("gpt-mini params = %+v", got)
	}
	if got := s.Targets["fast"].Params.MaxTokens; got != 256 {
		t.Fatalf("default max tokens not applied: %d", got)
	}
	if diff := cmp.Diff([]string{"claude", "fast", "gpt-mini", "llama"}, s.MatrixTargets()); diff != "" {
		t.Fatalf("matrix excludes judge (-want +got):\n%s", diff)
	}
	if s.Prompts["greet"] != "hi {{.name}}" {
		t.Fatalf("prompt file not inlined: %q", s.Prompts["greet"])
	}
	params, ok := s.Tools["lookup"].Parameters.(map[string]any)
	if !ok || params["type"] != "object" {
		t.Fatalf("tool schema not loaded: %#v", s.Tools["lookup"].Parameters)
	}
	if s.Compare[0].Mode != "regression" || s.Compare[0].Margin != 0.05 {
		t.Fatalf("compare defaults = %+v", s.Compare[0])
	}
	if s.Compare[1].Mode != "noninferiority" || s.Compare[1].Margin != 0.1 {
		t.Fatalf("compare[1] = %+v", s.Compare[1])
	}
	g := s.Gates
	if g.Alpha != 0.01 || g.MinEffect != 0.05 || *g.MinPassRate != 0.9 || *g.MaxErrorRate != 0.1 ||
		*g.Cost.MaxIncreasePct != 20 || g.Baseline != ".evalvet/base.json" {
		t.Fatalf("gates = %+v", g)
	}

	ids := make([]string, len(s.Cases))
	for i, c := range s.Cases {
		ids[i] = c.ID
	}
	wantIDs := []string{"single", "multi", "rows#a", "rows#002", "rows#c", "docs#x", "docs#y1", "docs#003"}
	if diff := cmp.Diff(wantIDs, ids); diff != "" {
		t.Fatalf("case ids (-want +got):\n%s", diff)
	}
}

func TestParseFullSuiteRendering(t *testing.T) {
	t.Parallel()
	s := parseFull(t, noEnv)
	byID := map[string]Case{}
	for _, c := range s.Cases {
		byID[c.ID] = c
	}

	single := byID["single"]
	if single.System != "You are tester" || single.Samples != 5 {
		t.Fatalf("single = %+v", single)
	}
	if diff := cmp.Diff([]string{"hi Ada"}, single.Messages); diff != "" {
		t.Fatalf("messages (-want +got):\n%s", diff)
	}
	if single.Data["blob"] != "BLOB" {
		t.Fatalf("vars file ref not resolved: %v", single.Data["blob"])
	}
	a := single.Asserts
	if a[0].Value != "hi Ada" || a[2].Pattern != "^hi Ada$" || a[5].Equals != "Ada" || a[5].Regex != "xAda" {
		t.Fatalf("rendered asserts = %+v", a[:6])
	}
	if schema, ok := a[4].Schema.(map[string]any); !ok || schema["type"] != "object" {
		t.Fatalf("assert schema not loaded: %#v", a[4].Schema)
	}
	if a[6].Kind != "not" || a[6].Not.Value != "bad Ada" {
		t.Fatalf("not assert = %+v", a[6])
	}
	if a[7].Kind != "not" || a[7].Not.Kind != "regex" {
		t.Fatalf("implicit not = %+v", a[7])
	}

	multi := byID["multi"]
	if diff := cmp.Diff([]string{"first why", "Say Bob"}, multi.Messages); diff != "" {
		t.Fatalf("turns (-want +got):\n%s", diff)
	}
	if multi.Agent == nil || multi.Agent.MaxSteps != 8 || multi.Agent.MaxToolCalls != 32 {
		t.Fatalf("agent defaults = %+v", multi.Agent)
	}
	if multi.Asserts[0].Args["id"] != "why" {
		t.Fatalf("args = %v", multi.Asserts[0].Args)
	}
	if multi.Asserts[5].Rubric[0].Pass != "mentions why" {
		t.Fatalf("rubric = %+v", multi.Asserts[5].Rubric)
	}
	if multi.Asserts[0].Times.Min == nil || *multi.Asserts[0].Times.Max != 2 {
		t.Fatalf("times = %+v", multi.Asserts[0].Times)
	}

	rows := []struct{ id, msg, want string }{
		{"rows#a", "Q one / 1", "1"},
		{"rows#002", "Q two / 2", "2"},
		{"rows#c", "Q three / 3", "3"},
		{"docs#x", "D alpha", "A"},
		{"docs#y1", "D beta", "B"},
		{"docs#003", "D gamma", "G"},
	}
	for _, r := range rows {
		c := byID[r.id]
		if len(c.Messages) != 1 || c.Messages[0] != r.msg {
			t.Fatalf("%s messages = %v want %q", r.id, c.Messages, r.msg)
		}
		if c.Asserts[0].Value != r.want {
			t.Fatalf("%s expected = %v want %q", r.id, c.Asserts[0].Value, r.want)
		}
		if c.Data["case_id"] != r.id {
			t.Fatalf("%s case_id = %v", r.id, c.Data["case_id"])
		}
		if c.Samples != 2 {
			t.Fatalf("%s samples = %d", r.id, c.Samples)
		}
	}
}

func TestEnvExpansionOnlyModelAndBaseURL(t *testing.T) {
	t.Parallel()
	t.Run("defaults used when unset", func(t *testing.T) {
		t.Parallel()
		s := parseFull(t, noEnv)
		if s.Targets["fast"].Model != "m1" || s.Providers["mock"].BaseURL != "http://mock.local" {
			t.Fatalf("model=%q base=%q", s.Targets["fast"].Model, s.Providers["mock"].BaseURL)
		}
	})
	t.Run("env overrides default", func(t *testing.T) {
		t.Parallel()
		s := parseFull(t, mapEnv(map[string]string{"FAST_MODEL": "m2", "MOCK_URL": "http://x"}))
		if s.Targets["fast"].Model != "m2" || s.Providers["mock"].BaseURL != "http://x" {
			t.Fatalf("model=%q base=%q", s.Targets["fast"].Model, s.Providers["mock"].BaseURL)
		}
	})
	t.Run("empty env value falls back to default", func(t *testing.T) {
		t.Parallel()
		s := parseFull(t, mapEnv(map[string]string{"FAST_MODEL": ""}))
		if s.Targets["fast"].Model != "m1" {
			t.Fatalf("model=%q", s.Targets["fast"].Model)
		}
	})
	t.Run("other fields untouched", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(baseYAML, "prompt: hello", "prompt: ${HOME_VAR}", 1)
		doc = strings.Replace(doc, "suite: demo", "suite: demo\ndescription: ${HOME_VAR}", 1)
		s, err := Parse("s.yaml", []byte(doc), mapEnv(map[string]string{"HOME_VAR": "leak"}))
		if err != nil {
			t.Fatal(err)
		}
		if s.Tests[0].Prompt != "${HOME_VAR}" || s.Description != "${HOME_VAR}" {
			t.Fatalf("prompt=%q description=%q", s.Tests[0].Prompt, s.Description)
		}
	})
}

func TestExpandEnv(t *testing.T) {
	t.Parallel()
	env := mapEnv(map[string]string{"A": "1", "EMPTY": ""})
	tests := []struct{ in, want string }{
		{"${A}", "1"},
		{"$A", "1"},
		{"${B:-dflt}", "dflt"},
		{"${A:-dflt}", "1"},
		{"${EMPTY:-dflt}", "dflt"},
		{"${B}", ""},
		{"x-${A}-${B:-y}", "x-1-y"},
		{"plain", "plain"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := ExpandEnv(tc.in, env); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestParseNilEnvUsesProcessEnv(t *testing.T) {
	t.Setenv("EVALVET_TEST_MODEL", "from-process")
	doc := strings.Replace(baseYAML, "model: m1", "model: ${EVALVET_TEST_MODEL}", 1)
	s, err := Parse("s.yaml", []byte(doc), nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Targets["fast"].Model != "from-process" {
		t.Fatalf("model = %q", s.Targets["fast"].Model)
	}
}

func TestHashAndSeedStability(t *testing.T) {
	t.Parallel()
	a, err := Parse("s.yaml", []byte(baseYAML), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse("s.yaml", []byte(baseYAML), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == "" || a.Hash != b.Hash || a.Seed() != b.Seed() {
		t.Fatalf("hash unstable: %q vs %q", a.Hash, b.Hash)
	}

	reordered := `suite: demo
version: 1
tests:
  - assert:
      - value: hi
        kind: contains
    prompt: hello
    id: t1
  - assert:
      - value: bye
        kind: contains
    prompt: bye
    id: t2
targets:
  claude:
    model: claude-haiku-5-5
    provider: an
  gpt-mini:
    model: gpt-5-mini
    provider: oa
  fast:
    model: m1
    provider: mock
providers:
  an:
    api_key_env: ANTHROPIC_API_KEY
    kind: anthropic
  oa:
    api_key_env: OPENAI_API_KEY
    kind: openai
  mock:
    kind: mock
`
	c, err := Parse("other.yaml", []byte(reordered), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash != a.Hash || c.Seed() != a.Seed() {
		t.Fatalf("key order changed hash: %q vs %q", c.Hash, a.Hash)
	}

	changed := strings.Replace(baseYAML, "value: hi", "value: hello", 1)
	d, err := Parse("s.yaml", []byte(changed), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if d.Hash == a.Hash || d.Seed() == a.Seed() {
		t.Fatal("content change must change hash and seed")
	}
}

func TestSeedFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		hash string
		want uint64
	}{
		{"empty", "", 42},
		{"not hex", "zz", 42},
		{"too short", "abcd", 42},
		{"big endian first 8 bytes", "0000000000000102ffff", 258},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Suite{Hash: tc.hash}
			if got := s.Seed(); got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}

func TestDatasetErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		files   map[string]string
		each    string
		wantMsg string
	}{
		{"no match", nil, "data/*.jsonl", "matched no records"},
		{"bad extension", map[string]string{"data/a.csv": "x"}, "data/a.csv", "unsupported extension"},
		{"bad jsonl line", map[string]string{"data/a.jsonl": "{\"a\":1}\n{oops\n"}, "data/a.jsonl", "a.jsonl:2"},
		{"bad json", map[string]string{"data/a.json": "{oops"}, "data/a.json", "decode"},
		{"bad json array", map[string]string{"data/a.json": "[1,2]"}, "data/a.json", "decode"},
		{"empty file", map[string]string{"data/a.jsonl": "\n\n"}, "data/a.jsonl", "matched no records"},
		{"bad glob", nil, "data/[", "glob"},
		{
			"duplicate record ids",
			map[string]string{"data/a.jsonl": "{\"id\":\"x\"}\n{\"id\":\"x\"}\n"},
			"data/a.jsonl",
			"duplicate case id",
		},
		{
			"missing var in template",
			map[string]string{"data/a.jsonl": "{\"q\":1}\n"},
			"data/a.jsonl",
			"render template",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			files := map[string]string{}
			maps.Copy(files, tc.files)
			prompt := "x"
			if tc.name == "missing var in template" {
				prompt = "{{.missing}}"
			}
			doc := strings.Replace(baseYAML, "prompt: hello", "prompt: '"+prompt+"'\n    each: "+tc.each, 1)
			dir := writeFiles(t, files)
			_, err := Parse(filepath.Join(dir, "s.yaml"), []byte(doc), noEnv)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("want error containing %q, got %v", tc.wantMsg, err)
			}
			if ve, ok := AsValidation(err); !ok || ve == nil {
				t.Fatalf("want validation error, got %T", err)
			}
		})
	}
}

func TestJSONDatasetSingleObjectUsesFilenameAsID(t *testing.T) {
	t.Parallel()
	dir := writeFiles(t, map[string]string{"data/case-one.json": `{"topic":"t","expected":"e"}`})
	doc := strings.Replace(baseYAML, "prompt: hello", "prompt: '{{.topic}}'\n    each: file://data/*.json", 1)
	s, err := Parse(filepath.Join(dir, "s.yaml"), []byte(doc), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if s.Cases[0].ID != "t1#case-one" || s.Cases[0].Messages[0] != "t" {
		t.Fatalf("case = %+v", s.Cases[0])
	}
}

func TestAsInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		in     any
		want   int
		wantOK bool
	}{
		{"int", 3, 3, true},
		{"int64", int64(4), 4, true},
		{"uint64", uint64(5), 5, true},
		{"uint64 overflow", uint64(1 << 40), 0, false},
		{"whole float", 6.0, 6, true},
		{"fractional float", 1.5, 0, false},
		{"string", "1", 0, false},
		{"nil", nil, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := AsInt(tc.in)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("got %d,%v want %d,%v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestKeyQuoting(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"plain_1", ".plain_1"},
		{"gpt-mini", ".'gpt-mini'"},
		{"a.b", ".'a.b'"},
		{"", ".''"},
		{"it's", ".'its'"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := Key(tc.in); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAssertKindsContainsAllKinds(t *testing.T) {
	t.Parallel()
	got := AssertKinds()
	for _, k := range []string{
		"equals", "contains", "regex", "json_valid", "json_schema", "json_path", "not",
		"cost_max", "latency_max", "not_refused", "tool_called", "tool_not_called", "tool_order", "max_steps",
		"max_tool_calls", "judge",
	} {
		found := false
		for _, g := range got {
			found = found || g == k
		}
		if !found {
			t.Fatalf("missing kind %s", k)
		}
	}
}

func TestSuiteLineFallsBackToParent(t *testing.T) {
	t.Parallel()
	s, err := Parse("s.yaml", []byte(baseYAML), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := lineOf(t, baseYAML, "    provider: mock", false)
	if got := s.Line("$.targets.fast.provider"); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	if got := s.Line("$.targets.fast.params.temperature"); got == 0 {
		t.Fatal("expected fallback to an existing parent line")
	}
	if got := s.Line(""); got != 0 {
		t.Fatalf("empty path line = %d", got)
	}
	if got := s.Line("$.nonexistent"); got != 0 {
		t.Fatalf("missing path line = %d", got)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(baseYAML))
	f.Add([]byte(fullYAML))
	f.Add([]byte(""))
	f.Add([]byte("version: 1\nsuite: x\n"))
	f.Add([]byte("version: [\n"))
	f.Add([]byte("{{{{"))
	f.Add([]byte("providers: {a: {kind: mock}}\ntargets: {'x y': {provider: a, model: ${M:-q}}}\n"))
	f.Add([]byte(strings.Replace(baseYAML, "prompt: hello", "prompt: '{{.x'", 1)))
	f.Add([]byte(strings.Replace(baseYAML, "prompt: hello", "prompt: file:///etc/nonexistent", 1)))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		s, err := Parse(filepath.Join(dir, "s.yaml"), data, noEnv)
		if err != nil {
			if s != nil {
				t.Fatal("suite and error both returned")
			}
			if ve, ok := AsValidation(err); !ok || ve == nil {
				t.Fatalf("non-validation error %T: %v", err, err)
			}
			return
		}
		if s == nil || s.Hash == "" {
			t.Fatal("nil suite or empty hash without error")
		}
	})
}

package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

func assertKindList() []string {
	return []string{
	"equals", "contains", "regex", "json_valid", "json_schema", "json_path",
	"not", "cost_max", "latency_max", "not_refused",
	"tool_called", "tool_not_called", "tool_order", "max_steps", "max_tool_calls",
	"judge",
	}
}

func AssertKinds() []string {
	return assertKindList()
}

func agentKind(kind string) bool {
	switch kind {
	case "tool_called", "tool_not_called", "tool_order", "max_steps":
		return true
	}
	return false
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func (s *Suite) validate(c *collector) {
	if s.Version != 1 {
		c.add("$.version", "unsupported version %d, want 1", s.Version)
	}
	if !validName(s.Name) {
		c.add("$.suite", "suite name %q must be non-empty and use only letters, digits, '-', '_' or '.'", s.Name)
	}
	s.validateProviders(c)
	s.validateTargets(c)
	s.validateTools(c)
	judgeUsed := s.validateTests(c)
	s.validateMatrix(c)
	s.validateCompare(c)
	s.validateGates(c)
	if judgeUsed {
		if s.Defaults.Judge.Target == "" {
			c.add("$.defaults.judge", "judge assertions need defaults.judge.target")
		} else if _, ok := s.Targets[s.Defaults.Judge.Target]; !ok {
			c.add("$.defaults.judge.target", "unknown judge target %q", s.Defaults.Judge.Target)
		}
	}
}

func (s *Suite) validateProviders(c *collector) {
	if len(s.Providers) == 0 {
		c.add("$.providers", "at least one provider is required")
	}
	for _, name := range sortedKeys(s.Providers) {
		p := s.Providers[name]
		path := "$.providers" + Key(name)
		switch p.Kind {
		case KindOpenAI, KindAnthropic:
			if p.APIKeyEnv == "" {
				c.add(path, "api_key_env is required for kind %s", p.Kind)
			}
		case KindOpenAICompatible:
			if p.BaseURL == "" {
				c.add(path, "base_url is required for kind openai_compatible")
			}
		case KindMock:
		default:
			c.add(path+".kind", "unknown provider kind %q (want openai, openai_compatible, anthropic or mock)", p.Kind)
		}
		if p.APIKey != "" {
			c.add(path+".api_key", "inline API keys are not allowed; set api_key_env to the name of an environment variable")
		}
		if p.Limits.RPM < 0 || p.Limits.TPM < 0 || p.Limits.Concurrency < 0 {
			c.add(path+".limits", "limits must be non-negative")
		}
	}
}

func (s *Suite) validateTargets(c *collector) {
	if len(s.Targets) == 0 {
		c.add("$.targets", "at least one target is required")
	}
	for _, name := range sortedKeys(s.Targets) {
		t := s.Targets[name]
		path := "$.targets" + Key(name)
		if !validName(name) {
			c.add(path, "target name %q must use only letters, digits, '-', '_' or '.'", name)
		}
		p, ok := s.Providers[t.Provider]
		if !ok {
			c.add(path+".provider", "unknown provider %q", t.Provider)
			continue
		}
		if t.Model == "" {
			c.add(path+".model", "model is required")
		}
		if p.Kind == KindAnthropic {
			if t.Params.Temperature != nil {
				c.add(path+".params.temperature", "the Anthropic API rejects temperature since API version 2026-10-21; sample repeatedly instead and use params.anthropic.effort")
			}
			if t.Params.Seed != nil {
				c.add(path+".params.seed", "seed is not supported by the Anthropic API")
			}
			if t.Params.OpenAI.ReasoningEffort != "" {
				c.add(path+".params.openai", "openai params on an anthropic target")
			}
		} else if t.Params.Anthropic.Effort != "" {
			c.add(path+".params.anthropic", "anthropic params on a %s target", p.Kind)
		}
		if e := t.Params.Anthropic.Effort; e != "" && !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, e) {
			c.add(path+".params.anthropic.effort", "unknown effort %q", e)
		}
		if e := t.Params.OpenAI.ReasoningEffort; e != "" && !slices.Contains([]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, e) {
			c.add(path+".params.openai.reasoning_effort", "unknown reasoning_effort %q", e)
		}
	}
}

func (s *Suite) validateTools(c *collector) {
	for _, name := range sortedKeys(s.Tools) {
		t := s.Tools[name]
		path := "$.tools" + Key(name)
		if t.Parameters != nil {
			if _, ok := t.Parameters.(map[string]any); !ok {
				c.add(path+".parameters", "parameters must be a JSON Schema object")
			}
		}
		m := t.Mock
		n := 0
		for _, set := range []bool{m.Static != nil, len(m.Match) > 0, len(m.Sequence) > 0, m.Error != ""} {
			if set {
				n++
			}
		}
		if n != 1 {
			c.add(path+".mock", "exactly one of static, match, sequence or error is required")
		}
		if m.LatencyMS < 0 {
			c.add(path+".mock.latency_ms", "latency_ms must be non-negative")
		}
	}
}

func (s *Suite) validateTests(c *collector) bool {
	if len(s.Tests) == 0 {
		c.add("$.tests", "at least one test is required")
	}
	judgeUsed := false
	seen := map[string]bool{}
	for i, t := range s.Tests {
		path := fmt.Sprintf("$.tests[%d]", i)
		switch {
		case t.ID == "":
			c.add(path, "id is required")
		case strings.ContainsAny(t.ID, "#\n"):
			c.add(path+".id", "id %q must not contain '#'", t.ID)
		case seen[t.ID]:
			c.add(path+".id", "duplicate test id %q", t.ID)
		}
		seen[t.ID] = true
		if (t.Prompt == "") == (len(t.Turns) == 0) {
			c.add(path, "exactly one of prompt or turns is required")
		}
		if t.Samples < 0 {
			c.add(path+".samples", "samples must be non-negative")
		}
		if t.Agent != nil {
			for j, tool := range t.Agent.Tools {
				if _, ok := s.Tools[tool]; !ok {
					c.add(fmt.Sprintf("%s.agent.tools[%d]", path, j), "unknown tool %q", tool)
				}
			}
		}
		if len(t.Assert) == 0 {
			c.add(path, "at least one assertion is required")
		}
		for j := range t.Assert {
			if s.validateAssert(c, fmt.Sprintf("%s.assert[%d]", path, j), &t.Assert[j], t.Agent != nil) {
				judgeUsed = true
			}
		}
	}
	return judgeUsed
}

func (s *Suite) validateAssert(c *collector, path string, a *AssertSpec, agent bool) bool {
	if a.Not != nil {
		if a.Kind != "" && a.Kind != "not" {
			c.add(path, "not: cannot be combined with kind %q", a.Kind)
		}
		a.Kind = "not"
		return s.validateAssert(c, path+".not", a.Not, agent)
	}
	if !slices.Contains(assertKindList(), a.Kind) {
		c.add(path+".kind", "unknown assertion kind %q (known: %s)", a.Kind, strings.Join(assertKindList(), ", "))
		return false
	}
	if agentKind(a.Kind) && !agent {
		c.add(path, "%s needs an agent: block on the test", a.Kind)
	}
	need := func(ok bool, field string) {
		if !ok {
			c.add(path, "%s requires %s", a.Kind, field)
		}
	}
	switch a.Kind {
	case "equals", "contains":
		need(a.Value != nil, "value")
	case "regex":
		need(a.Pattern != "" || a.Value != nil, "pattern")
	case "json_schema":
		need(a.Schema != nil, "schema")
	case "json_path":
		need(a.Path != "", "path")
	case "cost_max":
		need(a.MaxUSD > 0, "max_usd")
	case "latency_max":
		need(a.MaxMS > 0, "max_ms")
	case "tool_called", "tool_not_called":
		need(a.Name != "", "name")
	case "tool_order":
		need(len(a.Sequence) > 0, "sequence")
	case "max_steps", "max_tool_calls":
		_, ok := AsInt(a.Value)
		need(ok, "an integer value")
	case "judge":
		need(len(a.Rubric) > 0, "rubric")
		for i, r := range a.Rubric {
			if r.ID == "" || r.Pass == "" {
				c.add(fmt.Sprintf("%s.rubric[%d]", path, i), "criterion needs id and pass")
			}
		}
		if a.Target != "" {
			if _, ok := s.Targets[a.Target]; !ok {
				c.add(path+".target", "unknown judge target %q", a.Target)
			}
			return false
		}
		return true
	}
	return false
}

func (s *Suite) judgeTargets() map[string]bool {
	out := map[string]bool{}
	if s.Defaults.Judge.Target != "" {
		out[s.Defaults.Judge.Target] = true
	}
	for _, t := range s.Tests {
		for _, a := range t.Assert {
			if a.Kind == "judge" && a.Target != "" {
				out[a.Target] = true
			}
		}
	}
	return out
}

func (s *Suite) validateMatrix(c *collector) {
	if len(s.Matrix.Targets) == 0 {
		judges := s.judgeTargets()
		for _, name := range sortedKeys(s.Targets) {
			if !judges[name] {
				s.Matrix.Targets = append(s.Matrix.Targets, name)
			}
		}
		if len(s.Matrix.Targets) == 0 {
			c.add("$.matrix", "no targets to run")
		}
		return
	}
	for i, name := range s.Matrix.Targets {
		if _, ok := s.Targets[name]; !ok {
			c.add(fmt.Sprintf("$.matrix.targets[%d]", i), "unknown target %q", name)
		}
	}
}

func (s *Suite) validateCompare(c *collector) {
	for i, cmpSpec := range s.Compare {
		path := fmt.Sprintf("$.compare[%d]", i)
		for _, name := range []string{cmpSpec.Base, cmpSpec.Head} {
			if !slices.Contains(s.Matrix.Targets, name) {
				c.add(path, "target %q is not in the matrix", name)
			}
		}
		if cmpSpec.Base == cmpSpec.Head {
			c.add(path, "base and head must differ")
		}
		if cmpSpec.Mode != "regression" && cmpSpec.Mode != "noninferiority" {
			c.add(path+".mode", "unknown mode %q (want regression or noninferiority)", cmpSpec.Mode)
		}
	}
}

func (s *Suite) validateGates(c *collector) {
	g := s.Gates
	if g.Alpha <= 0 || g.Alpha >= 1 {
		c.add("$.gates.alpha", "alpha must be in (0, 1)")
	}
	if g.MinEffect < 0 || g.MinEffect >= 1 {
		c.add("$.gates.min_effect", "min_effect must be in [0, 1)")
	}
	if g.MinPassRate != nil && (*g.MinPassRate < 0 || *g.MinPassRate > 1) {
		c.add("$.gates.min_pass_rate", "min_pass_rate must be in [0, 1]")
	}
	if g.MaxErrorRate != nil && (*g.MaxErrorRate < 0 || *g.MaxErrorRate > 1) {
		c.add("$.gates.max_error_rate", "max_error_rate must be in [0, 1]")
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

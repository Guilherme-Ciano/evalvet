package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

const filePrefix = "file://"

type LookupEnv func(string) (string, bool)

func Load(path string, env LookupEnv) (*Suite, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read suite %s: %w", path, err)
	}
	return Parse(path, raw, env)
}

func Parse(path string, raw []byte, env LookupEnv) (*Suite, error) {
	if env == nil {
		env = os.LookupEnv
	}
	s := &Suite{}
	if err := yaml.UnmarshalWithOptions(raw, s, yaml.DisallowUnknownField()); err != nil {
		return nil, &ValidationError{Fields: []FieldError{{
			File: path,
			Line: yamlErrorLine(err),
			Msg:  firstLine(yaml.FormatError(err, false, false)),
		}}}
	}
	s.File = path
	s.Dir = filepath.Dir(path)
	s.lines = newLineIndex(raw)
	s.applyDefaults()
	s.expandEnv(env)
	c := &collector{suite: s}
	s.resolveFiles(c)
	if err := c.err(); err != nil {
		return nil, err
	}
	s.validate(c)
	if err := c.err(); err != nil {
		return nil, err
	}
	s.expandCases(c)
	if err := c.err(); err != nil {
		return nil, err
	}
	s.Hash = s.computeHash()
	return s, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func yamlErrorLine(err error) int {
	msg := err.Error()
	if !strings.HasPrefix(msg, "[") {
		return 0
	}
	end := strings.IndexByte(msg, ':')
	if end < 0 {
		return 0
	}
	n := 0
	for _, r := range msg[1:end] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *Suite) applyDefaults() {
	d := &s.Defaults
	d.Samples = positiveOr(d.Samples, 3)
	if d.Timeout <= 0 {
		d.Timeout = 60 * time.Second
	}
	if d.MaxTokens <= 0 {
		d.MaxTokens = 1024
	}
	d.Judge.Samples = positiveOr(d.Judge.Samples, 1)
	if s.Gates.Alpha <= 0 {
		s.Gates.Alpha = 0.05
	}
	if s.Gates.MinEffect <= 0 {
		s.Gates.MinEffect = 0.03
	}
	for i := range s.Compare {
		if s.Compare[i].Mode == "" {
			s.Compare[i].Mode = "regression"
		}
		if s.Compare[i].Margin <= 0 {
			s.Compare[i].Margin = s.Gates.MinEffect
		}
	}
	for name, t := range s.Targets {
		if t.Params.MaxTokens <= 0 {
			t.Params.MaxTokens = d.MaxTokens
			s.Targets[name] = t
		}
	}
	for i := range s.Tests {
		if a := s.Tests[i].Agent; a != nil {
			a.MaxSteps = positiveOr(a.MaxSteps, 8)
			a.MaxToolCalls = positiveOr(a.MaxToolCalls, 32)
		}
	}
}

func positiveOr(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func ExpandEnv(v string, env LookupEnv) string {
	return os.Expand(v, func(key string) string {
		name, def, hasDef := strings.Cut(key, ":-")
		if val, ok := env(name); ok && val != "" {
			return val
		}
		if hasDef {
			return def
		}
		return ""
	})
}

func (s *Suite) expandEnv(env LookupEnv) {
	for name, p := range s.Providers {
		p.BaseURL = ExpandEnv(p.BaseURL, env)
		s.Providers[name] = p
	}
	for name, t := range s.Targets {
		t.Model = ExpandEnv(t.Model, env)
		s.Targets[name] = t
	}
}

func (s *Suite) path(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(s.Dir, rel)
}

func (s *Suite) readRef(v string) (string, bool, error) {
	if !strings.HasPrefix(v, filePrefix) {
		return v, false, nil
	}
	b, err := os.ReadFile(s.path(strings.TrimPrefix(v, filePrefix)))
	if err != nil {
		return "", true, fmt.Errorf("read %s: %w", v, err)
	}
	return string(b), true, nil
}

func (s *Suite) readJSONRef(v any) (any, error) {
	str, ok := v.(string)
	if !ok {
		return v, nil
	}
	text, isRef, err := s.readRef(str)
	if err != nil {
		return nil, err
	}
	if !isRef {
		return v, nil
	}
	var out any
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", str, err)
	}
	return out, nil
}

func (s *Suite) resolveFiles(c *collector) {
	for name, p := range s.Prompts {
		text, _, err := s.readRef(p)
		if err != nil {
			c.add("$.prompts"+Key(name), "%v", err)
			continue
		}
		s.Prompts[name] = text
	}
	for name, t := range s.Tools {
		params, err := s.readJSONRef(t.Parameters)
		if err != nil {
			c.add("$.tools"+Key(name)+".parameters", "%v", err)
			continue
		}
		t.Parameters = params
		s.Tools[name] = t
	}
	for i := range s.Tests {
		base := fmt.Sprintf("$.tests[%d]", i)
		for k, v := range s.Tests[i].Vars {
			str, ok := v.(string)
			if !ok {
				continue
			}
			text, _, err := s.readRef(str)
			if err != nil {
				c.add(base+".vars"+Key(k), "%v", err)
				continue
			}
			s.Tests[i].Vars[k] = text
		}
		for j := range s.Tests[i].Assert {
			s.resolveAssertFiles(c, fmt.Sprintf("%s.assert[%d]", base, j), &s.Tests[i].Assert[j])
		}
	}
}

func (s *Suite) resolveAssertFiles(c *collector, path string, a *AssertSpec) {
	if a.Schema != nil {
		v, err := s.readJSONRef(a.Schema)
		if err != nil {
			c.add(path+".schema", "%v", err)
		} else {
			a.Schema = v
		}
	}
	if a.Not != nil {
		s.resolveAssertFiles(c, path+".not", a.Not)
	}
}

func (s *Suite) computeHash() string {
	view := struct {
		Version   int                 `json:"version"`
		Name      string              `json:"suite"`
		Defaults  Defaults            `json:"defaults"`
		Providers map[string]Provider `json:"providers"`
		Targets   map[string]Target   `json:"targets"`
		Prompts   map[string]string   `json:"prompts"`
		Tools     map[string]Tool     `json:"tools"`
		Tests     []Test              `json:"tests"`
		Matrix    Matrix              `json:"matrix"`
		Compare   []CompareSpec       `json:"compare"`
		Gates     Gates               `json:"gates"`
	}{s.Version, s.Name, s.Defaults, s.Providers, s.Targets, s.Prompts, s.Tools, s.Tests, s.Matrix, s.Compare, s.Gates}
	b, err := canonicalJSON(view)
	if err != nil {
		b = fmt.Appendf(nil, "%#v", view)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Suite) Seed() uint64 {
	b, err := hex.DecodeString(s.Hash)
	if err != nil || len(b) < 8 {
		return 42
	}
	var v uint64
	for _, x := range b[:8] {
		v = v<<8 | uint64(x)
	}
	return v
}

func (s *Suite) MatrixTargets() []string {
	return s.Matrix.Targets
}

func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode suite: %w", err)
	}
	return buf.Bytes(), nil
}

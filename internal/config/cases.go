package config

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/tmpl"
)

var errDataset = errors.New("invalid dataset")

func AsInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case uint64:
		if x > math.MaxInt32 {
			return 0, false
		}
		return int(x), true
	case float64:
		if x == math.Trunc(x) {
			return int(x), true
		}
	}
	return 0, false
}

func (s *Suite) promptText(ref string) string {
	if p, ok := s.Prompts[ref]; ok {
		return p
	}
	return ref
}

type record struct {
	id       string
	vars     map[string]any
	expected any
}

func (s *Suite) testRecords(t *Test) ([]record, error) {
	if t.Each == "" {
		return []record{{id: t.ID}}, nil
	}
	recs, err := s.loadRecords(t.Each)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("pattern %q matched no records: %w", t.Each, errDataset)
	}
	for j := range recs {
		if recs[j].id == "" {
			recs[j].id = fmt.Sprintf("%03d", j+1)
		}
		recs[j].id = t.ID + "#" + recs[j].id
	}
	return recs, nil
}

func (s *Suite) expandCases(c *collector) {
	for i := range s.Tests {
		t := &s.Tests[i]
		path := fmt.Sprintf("$.tests[%d]", i)
		records, err := s.testRecords(t)
		if err != nil {
			c.add(path+".each", "%v", err)
			continue
		}
		line := s.Line(path)
		for _, r := range records {
			cs, err := s.buildCase(i, line, t, r)
			if err != nil {
				c.add(path, "case %s: %v", r.id, err)
				continue
			}
			s.Cases = append(s.Cases, cs)
		}
	}
	seen := map[string]bool{}
	for i := range s.Cases {
		if seen[s.Cases[i].ID] {
			c.add(fmt.Sprintf("$.tests[%d]", s.Cases[i].TestIdx), "duplicate case id %q", s.Cases[i].ID)
		}
		seen[s.Cases[i].ID] = true
	}
}

func (s *Suite) buildCase(idx, line int, t *Test, r record) (Case, error) {
	data := map[string]any{}
	maps.Copy(data, t.Vars)
	maps.Copy(data, r.vars)
	data["expected"] = r.expected
	data["case_id"] = r.id
	cs := Case{ID: r.id, TestIdx: idx, Line: line, Agent: t.Agent, Data: data}
	cs.Samples = t.Samples
	if cs.Samples <= 0 {
		cs.Samples = s.Defaults.Samples
	}
	var err error
	if t.System != "" {
		if cs.System, err = tmpl.Render(t.ID+".system", s.promptText(t.System), data); err != nil {
			return Case{}, err
		}
	}
	if t.Prompt != "" {
		msg, err := tmpl.Render(t.ID+".prompt", s.promptText(t.Prompt), data)
		if err != nil {
			return Case{}, err
		}
		cs.Messages = []string{msg}
	}
	for j, turn := range t.Turns {
		msg, err := tmpl.Render(fmt.Sprintf("%s.turns[%d]", t.ID, j), s.promptText(turn.User), data)
		if err != nil {
			return Case{}, err
		}
		cs.Messages = append(cs.Messages, msg)
	}
	for _, a := range t.Assert {
		ra, err := renderAssert(t.ID, a, data)
		if err != nil {
			return Case{}, err
		}
		cs.Asserts = append(cs.Asserts, ra)
	}
	return cs, nil
}

func renderAssert(id string, a AssertSpec, data map[string]any) (AssertSpec, error) {
	name := id + ".assert." + a.Kind
	var err error
	if a.Value, err = tmpl.RenderValue(name, a.Value, data); err != nil {
		return a, err
	}
	if a.Equals, err = tmpl.RenderValue(name, a.Equals, data); err != nil {
		return a, err
	}
	if a.Pattern, err = tmpl.Render(name, a.Pattern, data); err != nil {
		return a, err
	}
	if a.Regex, err = tmpl.Render(name, a.Regex, data); err != nil {
		return a, err
	}
	if a.Args != nil {
		v, err := tmpl.RenderValue(name, a.Args, data)
		if err != nil {
			return a, err
		}
		if m, ok := v.(map[string]any); ok {
			a.Args = m
		}
	}
	if len(a.Rubric) > 0 {
		rub := make([]Criterion, len(a.Rubric))
		for i, r := range a.Rubric {
			rub[i] = r
			if rub[i].Pass, err = tmpl.Render(name, r.Pass, data); err != nil {
				return a, err
			}
		}
		a.Rubric = rub
	}
	if a.Not != nil {
		inner, err := renderAssert(id, *a.Not, data)
		if err != nil {
			return a, err
		}
		a.Not = &inner
	}
	return a, nil
}

func (s *Suite) loadRecords(pattern string) ([]record, error) {
	pattern = strings.TrimPrefix(pattern, filePrefix)
	files, err := filepath.Glob(s.path(pattern))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	sort.Strings(files)
	var out []record
	for _, f := range files {
		recs, err := readRecordFile(f)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func decodeJSONL(path string, b []byte) ([]map[string]any, error) {
	var raws []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		raws = append(raws, m)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return raws, nil
}

func decodeJSONFile(path string, b []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var raws []map[string]any
		if err := json.Unmarshal(trimmed, &raws); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		return raws, nil
	}
	var m map[string]any
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if m["id"] == nil {
		m["id"] = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return []map[string]any{m}, nil
}

func readRecordFile(path string) ([]record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read dataset %s: %w", path, err)
	}
	var raws []map[string]any
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jsonl":
		raws, err = decodeJSONL(path, b)
	case ".json":
		raws, err = decodeJSONFile(path, b)
	default:
		err = fmt.Errorf("dataset %s: unsupported extension (want .json or .jsonl): %w", path, errDataset)
	}
	if err != nil {
		return nil, err
	}
	out := make([]record, 0, len(raws))
	for _, m := range raws {
		out = append(out, toRecord(m))
	}
	return out, nil
}

func toRecord(m map[string]any) record {
	r := record{vars: map[string]any{}}
	if id, ok := m["id"]; ok {
		r.id = fmt.Sprint(id)
	}
	r.expected = m["expected"]
	if v, ok := m["vars"].(map[string]any); ok {
		r.vars = v
		return r
	}
	for k, v := range m {
		if k != "expected" && k != "id" {
			r.vars[k] = v
		}
	}
	return r
}

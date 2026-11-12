package match

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"github.com/Guilherme-Ciano/evalvet/internal/cache"
	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type Matcher func(v any) bool

func ToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case uint64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func Equal(got, want any) bool {
	if _, isStr := got.(string); !isStr {
		if gf, ok := ToFloat(got); ok {
			if wf, ok := ToFloat(want); ok {
				return math.Abs(gf-wf) <= 1e-9*math.Max(1, math.Abs(wf))
			}
		}
	}
	if gs, ok := got.(string); ok {
		ws, ok := want.(string)
		return ok && gs == ws
	}
	a, errA := cache.Canonical(got)
	b, errB := cache.Canonical(want)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

func Compile(want any) (Matcher, error) {
	m, ok := want.(map[string]any)
	if !ok {
		return func(v any) bool { return Equal(v, want) }, nil
	}
	if pat, ok := m["regex"].(string); ok && len(m) == 1 {
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf("regex %q: %w: %w", pat, err, domain.ErrConfig)
		}
		return func(v any) bool {
			s, ok := v.(string)
			return ok && re.MatchString(s)
		}, nil
	}
	subs := make(map[string]Matcher, len(m))
	for k, sub := range m {
		f, err := Compile(sub)
		if err != nil {
			return nil, err
		}
		subs[k] = f
	}
	return func(v any) bool {
		got, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for k, f := range subs {
			if !f(got[k]) {
				return false
			}
		}
		return true
	}, nil
}

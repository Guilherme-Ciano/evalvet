package tmpl

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

func funcs() template.FuncMap {
	return template.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			if err != nil {
				return "", fmt.Errorf("json: %w", err)
			}
			return string(b), nil
		},
		"lower": strings.ToLower,
		"upper": strings.ToUpper,
		"trim":  strings.TrimSpace,
		"join": func(sep string, v []any) string {
			parts := make([]string, len(v))
			for i, x := range v {
				parts[i] = fmt.Sprint(x)
			}
			return strings.Join(parts, sep)
		},
		"default": func(def, v any) any {
			if v == nil || v == "" {
				return def
			}
			return v
		},
	}
}

func HasAction(s string) bool {
	return strings.Contains(s, "{{")
}

func Render(name, text string, data map[string]any) (string, error) {
	if !HasAction(text) {
		return text, nil
	}
	t, err := template.New(name).Funcs(funcs()).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("parse template %s: %w", name, err)
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("render template %s: %w", name, err)
	}
	return sb.String(), nil
}

func RenderValue(name string, v any, data map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		return Render(name, x, data)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := RenderValue(name, e, data)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := RenderValue(name, e, data)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

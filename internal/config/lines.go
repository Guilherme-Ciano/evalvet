package config

import (
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

type lineIndex struct {
	file *ast.File
}

func newLineIndex(raw []byte) lineIndex {
	f, err := parser.ParseBytes(raw, 0)
	if err != nil {
		return lineIndex{}
	}
	return lineIndex{file: f}
}

func isPlainKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if r != '_' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

func Key(k string) string {
	if isPlainKey(k) {
		return "." + k
	}
	return ".'" + strings.ReplaceAll(k, "'", "") + "'"
}

func (l lineIndex) line(path string) int {
	if l.file == nil || path == "" {
		return 0
	}
	p, err := yaml.PathString(path)
	if err != nil {
		return 0
	}
	n, err := p.FilterFile(l.file)
	if err != nil || n == nil || n.GetToken() == nil {
		return 0
	}
	return n.GetToken().Position.Line
}

func (s *Suite) Line(path string) int {
	for p := path; p != "" && p != "$"; p = parentPath(p) {
		if n := s.lines.line(p); n > 0 {
			return n
		}
	}
	return 0
}

func parentPath(p string) string {
	i := strings.LastIndexAny(p, ".[")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

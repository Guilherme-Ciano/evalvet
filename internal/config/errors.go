package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

type FieldError struct {
	File string
	Line int
	Path string
	Msg  string
}

func (e FieldError) Error() string {
	var sb strings.Builder
	sb.WriteString(e.File)
	if e.Line > 0 {
		fmt.Fprintf(&sb, ":%d", e.Line)
	}
	if e.Path != "" {
		sb.WriteString(": ")
		sb.WriteString(strings.TrimPrefix(e.Path, "$."))
	}
	sb.WriteString(": ")
	sb.WriteString(e.Msg)
	return sb.String()
}

type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Error()
	}
	return strings.Join(parts, "\n")
}

func (e *ValidationError) Is(target error) bool {
	return target == domain.ErrConfig
}

func AsValidation(err error) (*ValidationError, bool) {
	var v *ValidationError
	ok := errors.As(err, &v)
	return v, ok
}

type collector struct {
	suite  *Suite
	fields []FieldError
}

func (c *collector) add(path, format string, args ...any) {
	c.fields = append(c.fields, FieldError{
		File: c.suite.File,
		Line: c.suite.Line(path),
		Path: path,
		Msg:  fmt.Sprintf(format, args...),
	})
}

func (c *collector) err() error {
	if len(c.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: c.fields}
}

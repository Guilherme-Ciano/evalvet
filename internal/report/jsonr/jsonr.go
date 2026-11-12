package jsonr

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/Guilherme-Ciano/evalvet/internal/domain"
)

func Write(w io.Writer, r domain.RunResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("write json report: %w", err)
	}
	return nil
}

func Read(rd io.Reader) (domain.RunResult, error) {
	var r domain.RunResult
	if err := json.NewDecoder(rd).Decode(&r); err != nil {
		return r, fmt.Errorf("read json report: %w", err)
	}
	return r, nil
}

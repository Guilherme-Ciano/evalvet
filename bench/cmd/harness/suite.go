package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type suiteSpec struct {
	Name     string
	Cases    int
	Samples  int
	Provider string
}

const promptText = "Extract the vendor name, vendor VAT id, total, currency and date " +
	"from the invoice below. Reply with JSON only.\n\n{{ .document }}\n"

const suiteTemplate = `version: 1
suite: %s
defaults:
  samples: %d
  timeout: 30s
providers:
%s
targets:
  head:
    provider: p
    model: mock-good
    params: { max_tokens: 256 }
prompts:
  extract: file://prompt.md
tests:
  - id: inv
    prompt: extract
    each: file://cases.jsonl
    assert:
      - kind: json_valid
      - kind: json_path
        path: $.total
        equals: "{{ .expected.total }}"
      - kind: contains
        value: "{{ .expected.vendor }}"
matrix:
  targets: [head]
gates:
  alpha: 0.05
  min_effect: 0.03
`

func mockProviderYAML() string {
	return "  p:\n    kind: mock\n"
}

func httpProviderYAML(baseURL string, rpm, conc int) string {
	return fmt.Sprintf(
		"  p:\n    kind: openai_compatible\n    base_url: %s\n    limits: { rpm: %d, concurrency: %d }\n",
		baseURL, rpm, conc,
	)
}

type record struct {
	ID       string         `json:"id"`
	Document string         `json:"document"`
	Expected map[string]any `json:"expected"`
}

func writeSuite(dir string, s suiteSpec) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir suite: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prompt.md"), []byte(promptText), 0o600); err != nil {
		return "", fmt.Errorf("write prompt: %w", err)
	}
	if err := writeCases(filepath.Join(dir, "cases.jsonl"), s.Cases); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "suite.eval.yaml")
	body := fmt.Sprintf(suiteTemplate, s.Name, s.Samples, s.Provider)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write suite: %w", err)
	}
	return path, nil
}

func writeCases(path string, n int) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create cases: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for i := range n {
		vendor := fmt.Sprintf("Vendor %05d Lda", i)
		total := float64(1000+i*37%9000) + float64(i%100)/100
		vat := fmt.Sprintf("PT%09d", 100000000+i)
		doc := fmt.Sprintf(
			"INVOICE PT-%05d\nVendor: %s\nVAT ID: %s\nDate: 2026-06-05\nItems: %d\nTotal: %.2f\nCurrency: EUR",
			i, vendor, vat, 1+i%9, total,
		)
		r := record{
			ID:       fmt.Sprintf("%06d", i),
			Document: doc,
			Expected: map[string]any{"total": total, "vat_id": vat, "vendor": vendor},
		}
		if err := enc.Encode(r); err != nil {
			_ = f.Close()
			return fmt.Errorf("encode case: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flush cases: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close cases: %w", err)
	}
	return nil
}

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func basicTemplate() map[string]string {
	return map[string]string{
		"evals/basic.eval.yaml": `version: 1
suite: basic
description: Starter suite. Swap the mock provider for a real one when ready.

defaults:
  samples: 3
  timeout: 30s

providers:
  mock:
    kind: mock

targets:
  current:
    provider: mock
    model: mock-good

prompts:
  greet: file://prompts/greet.md

tests:
  - id: greeting
    prompt: greet
    vars: { name: Ada }
    assert:
      - kind: contains
        value: Ada
      - not:
          kind: regex
          pattern: "(?i)as an ai"

gates:
  baseline: .evalvet/baselines/basic.json
  alpha: 0.05
  min_effect: 0.03
`,
		"evals/prompts/greet.md": "Write a one-line friendly greeting for {{ .name }}.\n",
	}
}

func agentTemplate() map[string]string {
	return map[string]string{
		"evals/agent.eval.yaml": `version: 1
suite: agent
description: Refund agent with mock tools and trajectory assertions.

defaults:
  samples: 5
  judge:
    target: judge

providers:
  mock:
    kind: mock

targets:
  agent:
    provider: mock
    model: mock-good
  judge:
    provider: mock
    model: mock-judge

prompts:
  system: file://prompts/support_agent.md

tools:
  lookup_order:
    description: Fetch an order by id
    parameters: file://schemas/lookup_order.json
    mock:
      match:
        - when: { order_id: A-1001 }
          return: { status: delivered, total: 129.90, currency: EUR, days_since_delivery: 12 }
        - when: { order_id: { regex: "^B-" } }
          return: { error: not_found }
          is_error: true
  check_refund_policy:
    description: Return refund eligibility for an order
    parameters: file://schemas/order_only.json
    mock:
      static: { eligible: true, window_days: 30 }
  issue_refund:
    description: Issue a refund
    parameters: file://schemas/refund.json
    mock:
      static: { refund_id: R-77, status: queued }

tests:
  - id: refund-happy-path
    system: system
    turns:
      - user: "I want a refund for order A-1001, it arrived broken."
    agent: { tools: [lookup_order, check_refund_policy, issue_refund], max_steps: 6 }
    assert:
      - kind: tool_called
        name: lookup_order
        args: { order_id: A-1001 }
        times: { max: 1 }
      - kind: tool_order
        sequence: [lookup_order, check_refund_policy, issue_refund]
      - kind: max_tool_calls
        value: 4
      - kind: judge
        rubric:
          - id: confirms_refund
            pass: The reply confirms the refund and mentions R-77.

  - id: refund-unknown-order
    system: system
    turns:
      - user: "Refund order B-9999 please"
    agent: { tools: [lookup_order, check_refund_policy, issue_refund], max_steps: 4 }
    assert:
      - kind: tool_not_called
        name: issue_refund
      - kind: contains
        value: ["could not find", "not found"]
        any: true
        case_insensitive: true

matrix:
  targets: [agent]

gates:
  baseline: .evalvet/baselines/agent.json
`,
		"evals/prompts/support_agent.md": "You are a support agent. Always look the order up before acting. Only refund eligible orders and never invent policy terms.\n",
		"evals/schemas/lookup_order.json": `{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}
`,
		"evals/schemas/order_only.json": `{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}
`,
		"evals/schemas/refund.json": `{"type":"object","properties":{"order_id":{"type":"string"},"reason":{"type":"string"}},"required":["order_id"]}
`,
	}
}

func extractionTemplate() map[string]string {
	return map[string]string{
		"evals/extraction.eval.yaml": `version: 1
suite: extraction
description: Invoice extraction with JSON Schema and JSONPath checks over a dataset.

defaults:
  samples: 3

providers:
  mock:
    kind: mock

targets:
  current:
    provider: mock
    model: mock-good

prompts:
  extract: file://prompts/extract_invoice.md

tests:
  - id: invoice
    prompt: extract
    each: file://fixtures/invoices.jsonl
    assert:
      - kind: json_schema
        schema: file://schemas/invoice.json
      - kind: json_path
        path: $.total
        equals: "{{ .expected.total }}"
      - kind: json_path
        path: $.vendor.vat_id
        regex: "^[A-Z]{2}[0-9A-Z]{8,12}$"

gates:
  baseline: .evalvet/baselines/extraction.json
`,
		"evals/prompts/extract_invoice.md": "Extract vendor name, VAT id, total, currency and date from this invoice as JSON.\n\n{{ .document }}\n",
		"evals/schemas/invoice.json": `{"type":"object","required":["vendor","total","currency"],"properties":{"vendor":{"type":"object","required":["name","vat_id"],"properties":{"name":{"type":"string"},"vat_id":{"type":"string"}}},"total":{"type":"number"},"currency":{"type":"string","pattern":"^[A-Z]{3}$"},"date":{"type":"string"}}}
`,
		"evals/fixtures/invoices.jsonl": `{"id":"pt-001","document":"Vendor: Lisboa Tech Lda\nVAT ID: PT123456789\nDate: 2026-10-02\nTotal: 1499.00\nCurrency: EUR","expected":{"total":1499.00}}
{"id":"de-002","document":"Vendor: Berlin Bau GmbH\nVAT ID: DE811907980\nDate: 2026-10-05\nTotal: 320.50\nCurrency: EUR","expected":{"total":320.50}}
{"id":"es-003","document":"Vendor: Madrid Datos SL\nVAT ID: ESB12345678\nDate: 2026-10-09\nTotal: 87.20\nCurrency: EUR","expected":{"total":87.20}}
`,
	}
}

func workflowTemplate() string {
	return `name: evalvet
on:
  pull_request:
permissions:
  contents: read
  pull-requests: write
jobs:
  evals:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: Guilherme-Ciano/evalvet@v0
        with:
          suites: evals/*.eval.yaml
`
}

func templates() map[string]func() map[string]string {
	return map[string]func() map[string]string{
		"basic":      basicTemplate,
		"agent":      agentTemplate,
		"extraction": extractionTemplate,
	}
}

func newInitCmd(c *cli) *cobra.Command {
	var (
		tmpl     string
		dir      string
		workflow bool
		force    bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold a starter suite that runs offline against the mock provider",
		RunE: func(_ *cobra.Command, _ []string) error {
			gen, ok := templates()[tmpl]
			if !ok {
				return usageError("unknown template %q (want basic, agent or extraction)", tmpl)
			}
			files := gen()
			if workflow {
				files[".github/workflows/evalvet.yml"] = workflowTemplate()
			}
			names := make([]string, 0, len(files))
			for n := range files {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if err := writeNew(filepath.Join(dir, n), files[n], force); err != nil {
					return err
				}
				fmt.Fprintf(c.streams.Out, "created %s\n", filepath.Join(dir, n))
			}
			if err := ensureGitignore(filepath.Join(dir, ".gitignore")); err != nil {
				return err
			}
			suite := names[0]
			for _, n := range names {
				if strings.HasSuffix(n, ".eval.yaml") {
					suite = n
				}
			}
			fmt.Fprintf(c.streams.Out, "\nnext: evalvet run %s --save-baseline .evalvet/baselines/%s.json\n", filepath.Join(dir, suite), tmpl)
			return nil
		},
	}
	cmd.Flags().StringVar(&tmpl, "template", "basic", "basic, agent or extraction")
	cmd.Flags().StringVar(&dir, "dir", ".", "target directory")
	cmd.Flags().BoolVar(&workflow, "workflow", false, "also write .github/workflows/evalvet.yml")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing files")
	return cmd
}

func writeNew(path, content string, force bool) error {
	if _, err := os.Stat(path); err == nil && !force {
		return usageError("%s already exists (use --force to overwrite)", path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func ensureGitignore(path string) error {
	want := []string{".evalvet/cache/", ".evalvet/out/"}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	have := string(raw)
	var add []string
	for _, w := range want {
		if !strings.Contains(have, w) {
			add = append(add, w)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if have != "" && !strings.HasSuffix(have, "\n") {
		have += "\n"
	}
	have += strings.Join(add, "\n") + "\n"
	if err := os.WriteFile(path, []byte(have), 0o600); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}
	return nil
}

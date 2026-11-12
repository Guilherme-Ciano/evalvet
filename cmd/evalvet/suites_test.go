package main

import (
	"path/filepath"
	"testing"
)

const tinyMockSuite = `version: 1
suite: tiny
defaults: { samples: 2 }
providers:
  mock: { kind: mock }
targets:
  t: { provider: mock, model: mock-good, params: { max_tokens: 64 } }
prompts:
  p: "Say hello to {{ .name }}"
tests:
  - id: hi
    prompt: p
    vars: { name: Ada }
    assert:
      - kind: contains
        value: Ada
`

const keyedSuite = `version: 1
suite: keyed
defaults: { samples: 1 }
providers:
  oa: { kind: openai, api_key_env: OPENAI_API_KEY }
targets:
  t: { provider: oa, model: gpt-5-mini }
prompts:
  p: "Say hello"
tests:
  - id: hi
    prompt: p
    assert:
      - kind: contains
        value: hello
`

const brokenYAML = "version: 1\nsuite: [unclosed\n  providers: {\n"

const unknownKindSuite = `version: 1
suite: bad
providers:
  mock: { kind: telepathy }
targets:
  t: { provider: mock, model: m }
prompts:
  p: "x"
tests:
  - id: a
    prompt: p
    assert:
      - kind: contains
        value: x
`

func suiteFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	writeFile(t, p, content)
	return p
}

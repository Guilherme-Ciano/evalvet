package transport_test

import (
	"encoding/json"
	"strconv"
	"testing"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func quote(t *testing.T, s string) string {
	t.Helper()
	return mustJSON(t, s)
}

func itoa(i int) string { return strconv.Itoa(i) }

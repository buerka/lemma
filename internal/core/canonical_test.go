package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalJSONSortsKeysAndIsStable(t *testing.T) {
	a, err := CanonicalJSON(map[string]any{"b": 1, "a": map[string]any{"d": true, "c": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalJSON(map[string]any{"a": map[string]any{"c": "x", "d": true}, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"a":{"c":"x","d":true},"b":1}` {
		t.Fatalf("unexpected canonical json: %s", a)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical json differs: %s vs %s", a, b)
	}
	if _, err := CanonicalJSON([]any{json.Number("0.10")}); err != nil {
		t.Fatal(err)
	}
	h1, err := HashValue(map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashValue(map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || !strings.HasPrefix(h1, "sha256:") || len(h1) != len("sha256:")+64 {
		t.Fatalf("unstable hash: %s vs %s", h1, h2)
	}
}

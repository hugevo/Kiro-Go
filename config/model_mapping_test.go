package config

import (
	"path/filepath"
	"testing"
)

func seedModelMappingConfig(t *testing.T) {
	t.Helper()
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
}

func TestMapModelForUpstreamPassthroughWhenEmpty(t *testing.T) {
	seedModelMappingConfig(t)
	for _, in := range []string{"claude-sonnet-4.5", "gpt-5.6-sol", ""} {
		if got := MapModelForUpstream(in); got != in {
			t.Fatalf("MapModelForUpstream(%q) = %q, want passthrough", in, got)
		}
	}
}

func TestMapModelForUpstreamRewritesEnabledMatch(t *testing.T) {
	seedModelMappingConfig(t)
	if err := UpdateModelMappings([]ModelMapping{
		{Facing: "claude-fable-5", Destination: "gpt-5.6-sol", Enabled: true, MaxTokens: 272000},
		{Facing: " my-alias ", Destination: "claude-sonnet-4.5", Enabled: true}, // trim
		{Facing: "disabled-alias", Destination: "claude-opus-4.6", Enabled: false},
	}); err != nil {
		t.Fatalf("update mappings: %v", err)
	}

	cases := map[string]string{
		"claude-fable-5":    "gpt-5.6-sol",       // exact match
		"Claude-Fable-5":    "gpt-5.6-sol",       // case-insensitive
		"  claude-fable-5 ": "gpt-5.6-sol",       // surrounding whitespace
		"my-alias":          "claude-sonnet-4.5", // trimmed facing matches
		"disabled-alias":    "disabled-alias",    // disabled rule → passthrough
		"unknown-model":     "unknown-model",     // no match → passthrough
	}
	for in, want := range cases {
		if got := MapModelForUpstream(in); got != want {
			t.Fatalf("MapModelForUpstream(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetModelMappingMaxTokens(t *testing.T) {
	seedModelMappingConfig(t)
	if err := UpdateModelMappings([]ModelMapping{
		{Facing: "claude-fable-5", Destination: "gpt-5.6-sol", Enabled: true, MaxTokens: 272000},
		{Facing: "zero-dest", Destination: "claude-opus-4.6", Enabled: true, MaxTokens: 0}, // override disabled
		{Facing: "disabled-dest", Destination: "claude-haiku-4.5", Enabled: false, MaxTokens: 999999},
	}); err != nil {
		t.Fatalf("update mappings: %v", err)
	}

	cases := map[string]int{
		"gpt-5.6-sol":      272000, // enabled, positive override
		"GPT-5.6-SOL":      272000, // case-insensitive
		"  gpt-5.6-sol ":   272000, // trimmed
		"claude-opus-4.6":  0,      // MaxTokens is 0 → no override
		"claude-haiku-4.5": 0,      // disabled mapping → ignored
		"unknown":          0,      // no mapping → 0
		"":                 0,
	}
	for dest, want := range cases {
		if got := GetModelMappingMaxTokens(dest); got != want {
			t.Fatalf("GetModelMappingMaxTokens(%q) = %d, want %d", dest, got, want)
		}
	}
}

func TestUpdateModelMappingsAssignsIDsAndPersists(t *testing.T) {
	seedModelMappingConfig(t)
	entries := []ModelMapping{
		{Facing: "a", Destination: "claude-a", Enabled: true},
		{Facing: "b", Destination: "claude-b", Enabled: true},
	}
	if err := UpdateModelMappings(entries); err != nil {
		t.Fatalf("update mappings: %v", err)
	}

	got := GetModelMappings()
	if len(got) != 2 {
		t.Fatalf("expected 2 mappings persisted, got %d", len(got))
	}
	for _, m := range got {
		if m.ID == "" {
			t.Fatalf("expected ID to be assigned, got empty for facing %q", m.Facing)
		}
	}

	// Reload to confirm persistence to disk.
	if err := Load(); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if len(GetModelMappings()) != 2 {
		t.Fatalf("expected 2 mappings after reload, got %d", len(GetModelMappings()))
	}
}

func TestGetModelMappingsReturnsCopy(t *testing.T) {
	seedModelMappingConfig(t)
	if err := UpdateModelMappings([]ModelMapping{
		{Facing: "a", Destination: "claude-a", Enabled: true},
	}); err != nil {
		t.Fatalf("update mappings: %v", err)
	}

	got := GetModelMappings()
	got[0].Facing = "mutated"
	// A second call must not observe the in-place mutation.
	if again := GetModelMappings(); again[0].Facing == "mutated" {
		t.Fatalf("GetModelMappings did not return an independent copy")
	}
}

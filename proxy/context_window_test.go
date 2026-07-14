package proxy

import (
	"kiro-go/config"
	"path/filepath"
	"testing"
)

// TestGetContextWindowSize verifies models are classified into the correct
// context window. This drives the input-token count that clients use to decide
// when to compact; misclassifying opus-4.8 (1M) as 200K under-reports tokens by
// 5x and prevents timely compaction.
func TestGetContextWindowSize(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"claude-opus-4.8", 1_000_000},
		{"claude-opus-4-8", 1_000_000},
		{"claude-opus-4.7", 1_000_000},
		{"claude-opus-4.6", 1_000_000},
		{"claude-sonnet-4.6", 1_000_000},
		{"claude-opus-4.8-thinking", 1_000_000},
		{"CLAUDE-OPUS-4.8", 1_000_000},
		{"claude-opus-4.5", 200_000},
		{"claude-sonnet-4.5", 200_000},
		{"claude-sonnet-4", 200_000},
		{"claude-haiku-4.5", 200_000},
		{"claude-3-5-sonnet", 200_000},
		// GPT 5.6 variants ship with a 272K window.
		{"gpt-5.6-sol", 272_000},
		{"gpt-5.6-luna", 272_000},
		{"gpt-5.6-terra", 272_000},
		{"gpt-5.6", 272_000},
		{"GPT-5.6-SOL", 272_000},
		{"unknown-model", 200_000},
	}
	for _, c := range cases {
		if got := getContextWindowSize(c.model); got != c.want {
			t.Errorf("getContextWindowSize(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

// TestGetContextWindowSizeForModel verifies the 3-tier resolution:
// tier 1 = model-mapping MaxTokens override, tier 2 = built-in tables
// (claude/gpt), tier 3 = default 200K.
func TestGetContextWindowSizeForModel(t *testing.T) {
	if err := config.Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}

	// Without an override, behavior matches getContextWindowSize.
	if got := getContextWindowSizeForModel("claude-sonnet-4.6"); got != 1_000_000 {
		t.Fatalf("claude-sonnet-4.6 = %d, want 1_000_000", got)
	}
	if got := getContextWindowSizeForModel("gpt-5.6-sol"); got != 272_000 {
		t.Fatalf("gpt-5.6-sol = %d, want 272_000", got)
	}
	if got := getContextWindowSizeForModel("totally-unknown"); got != 200_000 {
		t.Fatalf("unknown = %d, want 200_000", got)
	}

	// Seed a mapping whose destination carries an explicit MaxTokens override.
	if err := config.UpdateModelMappings([]config.ModelMapping{
		{Facing: "claude-fable-5", Destination: "gpt-5.6-sol", Enabled: true, MaxTokens: 300_000},
	}); err != nil {
		t.Fatalf("update mappings: %v", err)
	}

	// The override wins for the destination, even though the built-in table
	// would normally return 272_000 for gpt-5.6-sol.
	if got := getContextWindowSizeForModel("gpt-5.6-sol"); got != 300_000 {
		t.Fatalf("override gpt-5.6-sol = %d, want 300_000", got)
	}
	// An unrelated model still uses the built-in table.
	if got := getContextWindowSizeForModel("claude-sonnet-4.6"); got != 1_000_000 {
		t.Fatalf("claude-sonnet-4.6 with override on a different dest = %d, want 1_000_000", got)
	}
}

package proxy

import (
	"kiro-go/config"
	"strings"
	"testing"
)

// offCfg / onCfg are the two toggle states the resolvers branch on. Suffix is
// held at the default so suffix-fallback behavior is exercised consistently.
var (
	offCfg = config.ThinkingConfig{Suffix: "-thinking", ClaudeFormat: "thinking", OpenAIFormat: "reasoning_content", Passthrough: false}
	onCfg  = config.ThinkingConfig{Suffix: "-thinking", ClaudeFormat: "thinking", OpenAIFormat: "reasoning_content", Passthrough: true}
)

// TestThinkingDirectiveRenderLegacy locks the OFF / suffix-fallback directive to
// the exact historical prompt so passthrough-OFF stays byte-for-byte compatible.
func TestThinkingDirectiveRenderLegacy(t *testing.T) {
	// A bare enabled directive (no mode/budget) renders the legacy fixed prompt.
	got := ThinkingDirective{Enabled: true}.Render()
	if got != ThinkingModePrompt {
		t.Fatalf("legacy render mismatch:\n got %q\nwant %q", got, ThinkingModePrompt)
	}
	if !strings.Contains(got, "<max_thinking_length>200000</max_thinking_length>") {
		t.Fatalf("legacy render must carry fixed 200000 budget, got %q", got)
	}

	// A disabled directive renders nothing.
	if got := (ThinkingDirective{Enabled: false}).Render(); got != "" {
		t.Fatalf("disabled directive must render empty, got %q", got)
	}
}

func TestThinkingDirectiveRenderManualBudget(t *testing.T) {
	got := ThinkingDirective{Enabled: true, Mode: "enabled", BudgetTokens: 4096}.Render()
	want := "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>4096</max_thinking_length>"
	if got != want {
		t.Fatalf("manual budget render mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestThinkingDirectiveRenderAdaptiveEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		got := ThinkingDirective{Enabled: true, Mode: "adaptive", Effort: effort}.Render()
		want := "<thinking_mode>adaptive</thinking_mode>\n<thinking_effort>" + effort + "</thinking_effort>"
		if got != want {
			t.Fatalf("adaptive render mismatch for %q:\n got %q\nwant %q", effort, got, want)
		}
	}
	// Adaptive with no effort still renders the mode line.
	if got := (ThinkingDirective{Enabled: true, Mode: "adaptive"}).Render(); got != "<thinking_mode>adaptive</thinking_mode>" {
		t.Fatalf("adaptive-no-effort render mismatch, got %q", got)
	}
}

func TestValidateThinkingEffort(t *testing.T) {
	for _, ok := range []string{"", "low", "medium", "high", "xhigh", "max", "MAX", " high "} {
		if msg := validateThinkingEffort(ok); msg != "" {
			t.Fatalf("expected %q to be accepted, got error %q", ok, msg)
		}
	}
	for _, bad := range []string{"lowest", "ultra", "1", "none"} {
		if msg := validateThinkingEffort(bad); msg == "" {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

// TestResolveClaudeThinkingDirectiveOFF verifies OFF preserves the legacy boolean
// behavior: suffix or thinking request → fixed prompt, effort ignored.
func TestResolveClaudeThinkingDirectiveOFF(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		thinking    *ClaudeThinkingConfig
		effort      string
		wantEnabled bool
		wantRender  string
	}{
		{"suffix enables fixed", "claude-sonnet-4.5-thinking", nil, "", true, ThinkingModePrompt},
		{"enabled request fixed", "claude-sonnet-4.5", &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 4096}, "", true, ThinkingModePrompt},
		{"adaptive request fixed", "claude-sonnet-4.5", &ClaudeThinkingConfig{Type: "adaptive"}, "", true, ThinkingModePrompt},
		{"effort ignored when off", "claude-sonnet-4.5", nil, "high", false, ""},
		{"disabled stays off", "claude-sonnet-4.5", &ClaudeThinkingConfig{Type: "disabled"}, "", false, ""},
		{"plain model off", "claude-sonnet-4.5", nil, "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, d := resolveClaudeThinkingDirective(tc.model, tc.thinking, tc.effort, offCfg)
			if d.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", d.Enabled, tc.wantEnabled)
			}
			if got := d.Render(); got != tc.wantRender {
				t.Fatalf("render = %q, want %q", got, tc.wantRender)
			}
		})
	}
}

// TestResolveClaudeThinkingDirectiveON verifies ON precedence: explicit client
// intent over suffix, manual budget preserved, effort preserved, budget > effort.
func TestResolveClaudeThinkingDirectiveON(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		thinking    *ClaudeThinkingConfig
		effort      string
		wantEnabled bool
		wantRender  string
	}{
		{
			name:        "manual budget preserved",
			model:       "claude-sonnet-4.5",
			thinking:    &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 4096},
			wantEnabled: true,
			wantRender:  "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>4096</max_thinking_length>",
		},
		{
			name:        "manual min budget preserved",
			model:       "claude-sonnet-4.5",
			thinking:    &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 1024},
			wantEnabled: true,
			wantRender:  "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>1024</max_thinking_length>",
		},
		{
			name:        "adaptive effort preserved",
			model:       "claude-sonnet-4.5",
			thinking:    &ClaudeThinkingConfig{Type: "adaptive"},
			effort:      "high",
			wantEnabled: true,
			wantRender:  "<thinking_mode>adaptive</thinking_mode>\n<thinking_effort>high</thinking_effort>",
		},
		{
			name:        "output_config effort without thinking type",
			model:       "claude-sonnet-4.5",
			effort:      "medium",
			wantEnabled: true,
			wantRender:  "<thinking_mode>adaptive</thinking_mode>\n<thinking_effort>medium</thinking_effort>",
		},
		{
			name:        "manual budget beats effort",
			model:       "claude-sonnet-4.5",
			thinking:    &ClaudeThinkingConfig{Type: "enabled", BudgetTokens: 8192},
			effort:      "low",
			wantEnabled: true,
			wantRender:  "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>8192</max_thinking_length>",
		},
		{
			name:        "explicit disabled overrides suffix",
			model:       "claude-sonnet-4.5-thinking",
			thinking:    &ClaudeThinkingConfig{Type: "disabled"},
			wantEnabled: false,
			wantRender:  "",
		},
		{
			name:        "suffix fallback keeps fixed budget",
			model:       "claude-sonnet-4.5-thinking",
			wantEnabled: true,
			wantRender:  ThinkingModePrompt,
		},
		{
			name:        "no input no directive",
			model:       "claude-sonnet-4.5",
			wantEnabled: false,
			wantRender:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, d := resolveClaudeThinkingDirective(tc.model, tc.thinking, tc.effort, onCfg)
			if d.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", d.Enabled, tc.wantEnabled)
			}
			if got := d.Render(); got != tc.wantRender {
				t.Fatalf("render = %q, want %q", got, tc.wantRender)
			}
		})
	}
}

func TestResolveOpenAIThinkingDirective(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.ThinkingConfig
		model       string
		effort      string
		wantEnabled bool
		wantRender  string
	}{
		{"off ignores effort", offCfg, "claude-sonnet-4.5", "high", false, ""},
		{"off suffix fixed", offCfg, "claude-sonnet-4.5-thinking", "", true, ThinkingModePrompt},
		{"on effort adaptive", onCfg, "claude-sonnet-4.5", "high", true, "<thinking_mode>adaptive</thinking_mode>\n<thinking_effort>high</thinking_effort>"},
		{"on suffix fallback fixed", onCfg, "claude-sonnet-4.5-thinking", "", true, ThinkingModePrompt},
		{"on no input none", onCfg, "claude-sonnet-4.5", "", false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, d := resolveOpenAIThinkingDirective(tc.model, tc.effort, tc.cfg)
			if d.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", d.Enabled, tc.wantEnabled)
			}
			if got := d.Render(); got != tc.wantRender {
				t.Fatalf("render = %q, want %q", got, tc.wantRender)
			}
		})
	}
}

// TestResolveOpenAIThinkingDirectiveEffortAllValues covers all five accepted
// effort values as adaptive thinking under ON.
func TestResolveOpenAIThinkingDirectiveEffortAllValues(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max"} {
		_, d := resolveOpenAIThinkingDirective("claude-sonnet-4.5", effort, onCfg)
		if !d.Enabled || d.Mode != "adaptive" || d.Effort != effort {
			t.Fatalf("effort %q: got %+v", effort, d)
		}
		want := "<thinking_mode>adaptive</thinking_mode>\n<thinking_effort>" + effort + "</thinking_effort>"
		if got := d.Render(); got != want {
			t.Fatalf("effort %q render = %q, want %q", effort, got, want)
		}
	}
}

// TestThinkingDirectiveNoDuplicateInSystemPrompt verifies the generated directive
// is prepended exactly once and does not overwrite the user's system text.
func TestThinkingDirectiveNoDuplicateInSystemPrompt(t *testing.T) {
	d := ThinkingDirective{Enabled: true, Mode: "enabled", BudgetTokens: 2048}
	got := buildClaudeSystemPrompt("user system text", d)

	rendered := d.Render()
	if strings.Count(got, "<thinking_mode>") != 1 {
		t.Fatalf("expected exactly one thinking_mode block, got %q", got)
	}
	if !strings.HasPrefix(got, rendered) {
		t.Fatalf("expected system prompt to start with directive, got %q", got)
	}
	if !strings.Contains(got, "user system text") {
		t.Fatalf("expected user system text preserved, got %q", got)
	}
}

// TestThinkingDirectiveDistinctBudgetsRenderDistinctly guards against cache
// aliasing: two different budgets must produce different rendered system prompts.
func TestThinkingDirectiveDistinctBudgetsRenderDistinctly(t *testing.T) {
	a := buildClaudeSystemPrompt("", ThinkingDirective{Enabled: true, Mode: "enabled", BudgetTokens: 1024})
	b := buildClaudeSystemPrompt("", ThinkingDirective{Enabled: true, Mode: "enabled", BudgetTokens: 8192})
	if a == b {
		t.Fatalf("distinct budgets rendered identically: %q", a)
	}

	lo := buildClaudeSystemPrompt("", ThinkingDirective{Enabled: true, Mode: "adaptive", Effort: "low"})
	hi := buildClaudeSystemPrompt("", ThinkingDirective{Enabled: true, Mode: "adaptive", Effort: "max"})
	if lo == hi {
		t.Fatalf("distinct efforts rendered identically: %q", lo)
	}
}

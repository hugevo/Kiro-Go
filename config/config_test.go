package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateSettingsPatchPreservesOmittedAPIKeyFields(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := UpdateSettings("proxy-api-key", true, "admin-password"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := UpdateSettingsPatch(nil, nil, "new-admin-password"); err != nil {
		t.Fatalf("patch settings: %v", err)
	}

	if got := GetApiKey(); got != "proxy-api-key" {
		t.Fatalf("expected API key to be preserved, got %q", got)
	}
	if !IsApiKeyRequired() {
		t.Fatalf("expected requireApiKey to stay enabled")
	}
	if got := GetPassword(); got != "new-admin-password" {
		t.Fatalf("expected password to update, got %q", got)
	}
}

func TestUpdateSettingsPatchCanExplicitlyDisableAPIKey(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}
	if err := UpdateSettings("proxy-api-key", true, "admin-password"); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	emptyKey := ""
	requireAPIKey := false
	if err := UpdateSettingsPatch(&emptyKey, &requireAPIKey, ""); err != nil {
		t.Fatalf("patch settings: %v", err)
	}

	if got := GetApiKey(); got != "" {
		t.Fatalf("expected API key to be cleared, got %q", got)
	}
	if IsApiKeyRequired() {
		t.Fatalf("expected requireApiKey to be disabled")
	}
	if got := GetPassword(); got != "admin-password" {
		t.Fatalf("expected password to be preserved, got %q", got)
	}
}

func TestResolveKiroBuildHashPrecedence(t *testing.T) {
	if err := Init(filepath.Join(t.TempDir(), "config.json")); err != nil {
		t.Fatalf("init config: %v", err)
	}

	// Test 1: Code default for known version
	hash := ResolveKiroBuildHash("0.12.333", "")
	expected := "2ecd375f32fb815800ae42b778607b3a4cb0ef89208f4d12b13080ede8c29795"
	if hash != expected {
		t.Errorf("expected code default %q, got %q", expected, hash)
	}

	// Test 2: Account override (HashSuffix) wins over code default
	accountOverride := "account-override-hash"
	hash = ResolveKiroBuildHash("0.12.333", accountOverride)
	if hash != accountOverride {
		t.Errorf("expected account override %q, got %q", accountOverride, hash)
	}

	// Test 3: UI override wins over code default
	uiOverride := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	if err := UpdateKiroClientSettings("0.12.333", "", "", map[string]string{"0.12.333": uiOverride}); err != nil {
		t.Fatalf("update kiro client settings: %v", err)
	}
	hash = ResolveKiroBuildHash("0.12.333", "")
	if hash != uiOverride {
		t.Errorf("expected UI override %q, got %q", uiOverride, hash)
	}

	// Test 4: Account override wins over UI override
	hash = ResolveKiroBuildHash("0.12.333", accountOverride)
	if hash != accountOverride {
		t.Errorf("expected account override %q over UI override, got %q", accountOverride, hash)
	}

	// Test 5: Unknown version falls back to sha256 hash
	unknownHash := ResolveKiroBuildHash("99.99.99", "")
	if unknownHash == "" {
		t.Error("expected fallback hash for unknown version, got empty string")
	}
	if len(unknownHash) != 64 {
		t.Errorf("expected 64-char hex fallback, got %d chars: %q", len(unknownHash), unknownHash)
	}
}

func TestKiroBuildHashOverridesPersistence(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	if err := Init(cfgFile); err != nil {
		t.Fatalf("init config: %v", err)
	}

	// Set overrides
	overrides := map[string]string{
		"0.13.0": "aaa111222333444555666777888999000aaabbbcccdddeeefff000111222333",
		"0.14.0": "bbb111222333444555666777888999000aaabbbcccdddeeefff000111222333",
	}
	if err := UpdateKiroClientSettings("0.13.0", "win32#10.0.22631", "22.22.0", overrides); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	// Verify in memory
	settings := GetKiroClientSettings()
	if settings.KiroVersion != "0.13.0" {
		t.Errorf("expected KiroVersion 0.13.0, got %q", settings.KiroVersion)
	}
	if settings.BuildHashes["0.13.0"] != overrides["0.13.0"] {
		t.Errorf("expected build hash for 0.13.0, got %q", settings.BuildHashes["0.13.0"])
	}
	if settings.BuildHashes["0.14.0"] != overrides["0.14.0"] {
		t.Errorf("expected build hash for 0.14.0, got %q", settings.BuildHashes["0.14.0"])
	}

	// Reload from disk and verify persistence
	if err := Init(cfgFile); err != nil {
		t.Fatalf("reinit config: %v", err)
	}
	settings = GetKiroClientSettings()
	if settings.KiroVersion != "0.13.0" {
		t.Errorf("after reload: expected KiroVersion 0.13.0, got %q", settings.KiroVersion)
	}
	if settings.BuildHashes["0.13.0"] != overrides["0.13.0"] {
		t.Errorf("after reload: expected build hash for 0.13.0, got %q", settings.BuildHashes["0.13.0"])
	}
}

// TestAccountAllowOverageMigration verifies that a config.json from before the
// upstream-Overages-switch refactor (which carried `allowOverage: true` per
// account) is migrated into OverageStatus="ENABLED" on first load, and that
// the legacy field is cleared so future saves don't re-emit it.
func TestAccountAllowOverageMigration(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")

	seed := map[string]interface{}{
		"password":      "p",
		"port":          8080,
		"host":          "0.0.0.0",
		"requireApiKey": false,
		"accounts": []map[string]interface{}{
			{"id": "acc-allow", "enabled": true, "allowOverage": true},
			{"id": "acc-deny", "enabled": true, "allowOverage": false},
			{"id": "acc-already-set", "enabled": true, "allowOverage": true, "overageStatus": "DISABLED"},
		},
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgFile, raw, 0600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := Init(cfgFile); err != nil {
		t.Fatalf("init: %v", err)
	}

	accounts := GetAccounts()
	byID := map[string]Account{}
	for _, a := range accounts {
		byID[a.ID] = a
	}

	if got := byID["acc-allow"].OverageStatus; got != "ENABLED" {
		t.Fatalf("expected acc-allow to migrate to OverageStatus=ENABLED, got %q", got)
	}
	if byID["acc-allow"].LegacyAllowOverage {
		t.Fatalf("expected legacy allowOverage to be cleared after migration")
	}
	if got := byID["acc-deny"].OverageStatus; got != "" {
		t.Fatalf("expected acc-deny to keep empty OverageStatus, got %q", got)
	}
	// Pre-set OverageStatus must win over the legacy field.
	if got := byID["acc-already-set"].OverageStatus; got != "DISABLED" {
		t.Fatalf("expected acc-already-set OverageStatus to be preserved, got %q", got)
	}
	if byID["acc-already-set"].LegacyAllowOverage {
		t.Fatalf("expected legacy field to still be cleared on acc-already-set")
	}

	// Re-read the file and confirm legacy field is gone (so it doesn't drift
	// back in on later saves).
	on_disk, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var reloaded struct {
		Accounts []map[string]interface{} `json:"accounts"`
	}
	if err := json.Unmarshal(on_disk, &reloaded); err != nil {
		t.Fatalf("decode reload: %v", err)
	}
	for _, a := range reloaded.Accounts {
		if _, ok := a["allowOverage"]; ok {
			t.Fatalf("expected allowOverage to be omitted from persisted file, got %+v", a)
		}
	}
}

// TestThinkingPassthroughDefaultsFalse verifies a fresh config, and a config
// file with no thinkingPassthrough field, both decode the toggle to false while
// leaving the existing thinking defaults intact.
func TestThinkingPassthroughDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	if err := Init(cfgFile); err != nil {
		t.Fatalf("init config: %v", err)
	}

	got := GetThinkingConfig()
	if got.Passthrough {
		t.Fatalf("expected Passthrough to default to false on a fresh config")
	}
	if got.Suffix != "-thinking" {
		t.Fatalf("expected default suffix -thinking, got %q", got.Suffix)
	}
	if got.OpenAIFormat != "reasoning_content" {
		t.Fatalf("expected default openaiFormat reasoning_content, got %q", got.OpenAIFormat)
	}
	if got.ClaudeFormat != "thinking" {
		t.Fatalf("expected default claudeFormat thinking, got %q", got.ClaudeFormat)
	}

	// A config file that predates the toggle (field absent) must still decode to false.
	seed := map[string]interface{}{
		"password":      "p",
		"port":          8080,
		"host":          "0.0.0.0",
		"requireApiKey": false,
		"thinkingSuffix": "-think",
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgFile, raw, 0600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := Init(cfgFile); err != nil {
		t.Fatalf("reinit config: %v", err)
	}
	if GetThinkingConfig().Passthrough {
		t.Fatalf("expected Passthrough false when field absent from config file")
	}
}

// TestThinkingPassthroughRoundTrips verifies the toggle persists true and false
// across reloads without disturbing the other thinking settings.
func TestThinkingPassthroughRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.json")
	if err := Init(cfgFile); err != nil {
		t.Fatalf("init config: %v", err)
	}

	if err := UpdateThinkingConfig("-thinking", "reasoning_content", "thinking", true); err != nil {
		t.Fatalf("update thinking config: %v", err)
	}
	if !GetThinkingConfig().Passthrough {
		t.Fatalf("expected Passthrough true after update")
	}

	// Reload from disk and confirm persistence of true.
	if err := Init(cfgFile); err != nil {
		t.Fatalf("reinit config: %v", err)
	}
	got := GetThinkingConfig()
	if !got.Passthrough {
		t.Fatalf("expected Passthrough true after reload")
	}
	if got.Suffix != "-thinking" || got.OpenAIFormat != "reasoning_content" || got.ClaudeFormat != "thinking" {
		t.Fatalf("expected existing thinking settings intact, got %+v", got)
	}

	// Flip back to false and confirm it persists.
	if err := UpdateThinkingConfig("-thinking", "reasoning_content", "thinking", false); err != nil {
		t.Fatalf("update thinking config (false): %v", err)
	}
	if err := Init(cfgFile); err != nil {
		t.Fatalf("reinit config (false): %v", err)
	}
	if GetThinkingConfig().Passthrough {
		t.Fatalf("expected Passthrough false after reload")
	}
}

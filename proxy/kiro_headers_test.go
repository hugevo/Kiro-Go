package proxy

import (
	"kiro-go/config"
	"regexp"
	"strings"
	"testing"
)

func TestBuildStreamingHeaderValuesAlignsWithKiroIDEFormat(t *testing.T) {
	account := &config.Account{}
	values := buildStreamingHeaderValues(account, "q.us-east-1.amazonaws.com")

	if values.Host != "q.us-east-1.amazonaws.com" {
		t.Fatalf("expected host to be preserved, got %q", values.Host)
	}
	if !strings.Contains(values.UserAgent, "aws-sdk-js/1.0.39") {
		t.Fatalf("expected streaming sdk version in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "api/codewhispererstreaming#1.0.39") {
		t.Fatalf("expected streaming API marker in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "m/N") {
		t.Fatalf("expected streaming mode marker in user agent, got %q", values.UserAgent)
	}
	// The suffix after KiroIDE-<version> MUST be a bare 64-hex build hash (the real
	// Kiro client sends a content hash identical across all installs of the same
	// version), NOT a random per-install UUID with dashes.
	if !strings.Contains(values.UserAgent, "KiroIDE-0.12.333-2ecd375f32fb815800ae42b778607b3a4cb0ef89208f4d12b13080ede8c29795") {
		t.Fatalf("expected kiro version and known build hash in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.AmzUserAgent, "aws-sdk-js/1.0.39 KiroIDE-0.12.333-2ecd375f32fb815800ae42b778607b3a4cb0ef89208f4d12b13080ede8c29795") {
		t.Fatalf("expected x-amz-user-agent to include version and known build hash, got %q", values.AmzUserAgent)
	}
	// Ensure the suffix is bare hex (no UUID dashes): KiroIDE-<ver>-<64 hex>.
	suffixRe := regexp.MustCompile(`KiroIDE-[0-9.]+-[0-9a-f]{64}`)
	if !suffixRe.MatchString(values.UserAgent) {
		t.Fatalf("user agent suffix must be bare 64-hex, got %q", values.UserAgent)
	}
}

func TestBuildRuntimeHeaderValuesUsesRuntimeAPIFormat(t *testing.T) {
	account := &config.Account{}
	values := buildRuntimeHeaderValues(account, "codewhisperer.us-east-1.amazonaws.com")

	if !strings.Contains(values.UserAgent, "aws-sdk-js/1.0.0") {
		t.Fatalf("expected runtime sdk version in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "api/codewhispererruntime#1.0.0") {
		t.Fatalf("expected runtime API marker in user agent, got %q", values.UserAgent)
	}
	if !strings.Contains(values.UserAgent, "m/N,E") {
		t.Fatalf("expected runtime mode marker in user agent, got %q", values.UserAgent)
	}
}

func TestBuildHeaderValuesHashSuffixIsBareHexNotUUID(t *testing.T) {
	// Regression guard: the suffix after KiroIDE-<version> must never look like a
	// UUID (8-4-4-4-12 with dashes). A random UUID there is a strong detection
	// signal because the real client sends a fixed content hash for everyone.
	account := &config.Account{}
	values := buildStreamingHeaderValues(account, "q.us-east-1.amazonaws.com")
	uuidRe := regexp.MustCompile(`KiroIDE-[0-9.]+-[0-9a-f]{8}-[0-9a-f]{4}-`)
	if uuidRe.MatchString(values.UserAgent) {
		t.Fatalf("user agent suffix looks like a UUID, real client sends a bare hash: %q", values.UserAgent)
	}
}

func TestResolveKiroBuildHashFallbackIsBareHex(t *testing.T) {
	// Versions not yet catalogued must still produce a bare-hex (non-UUID) suffix
	// so the shape matches the real client. Replace with the real hash once captured.
	h := config.ResolveKiroBuildHash("0.0.0-unknown", "")
	bareHex := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !bareHex.MatchString(h) {
		t.Fatalf("unknown-version fallback must be bare 64-hex, got %q", h)
	}
}

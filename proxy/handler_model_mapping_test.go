package proxy

import (
	"bytes"
	"encoding/json"
	"kiro-go/config"
	accountpool "kiro-go/pool"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestClaudeModelMappingRewritesUpstreamAndEchoesFacing drives the Claude
// /v1/messages entry point end-to-end with a mapping claude-fable-5 ->
// gpt-5.6-sol and asserts that:
//   - the destination model (gpt-5.6-sol) is what reaches the upstream payload,
//   - the response echoed back to the client carries the facing (claude-fable-5).
//
// The account has no cached model list, so pool routing uses the optimistic
// cold-start path (accountHasModel == true) and the request succeeds on the
// first attempt.
func TestClaudeModelMappingRewritesUpstreamAndEchoesFacing(t *testing.T) {
	cfgFile := t.TempDir() + "/config.json"
	if err := config.Init(cfgFile); err != nil {
		t.Fatalf("config.Init: %v", err)
	}
	if err := config.AddAccount(config.Account{
		ID:          "acct-1",
		Enabled:     true,
		AccessToken: "token-1",
		ProfileArn:  "arn:aws:codewhisperer:profile/first",
	}); err != nil {
		t.Fatalf("add account: %v", err)
	}
	if err := config.UpdatePreferredEndpoint("kiro"); err != nil {
		t.Fatalf("set preferred endpoint: %v", err)
	}
	if err := config.UpdateEndpointFallback(false); err != nil {
		t.Fatalf("disable endpoint fallback: %v", err)
	}
	if err := config.UpdateModelMappings([]config.ModelMapping{
		{Facing: "claude-fable-5", Destination: "gpt-5.6-sol", Enabled: true, MaxTokens: 272000},
	}); err != nil {
		t.Fatalf("seed model mapping: %v", err)
	}

	var upstreamModelID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&raw)
		// The upstream payload nests modelId under currentMessage.userInputMessage.
		if cs, ok := raw["conversationState"]; ok {
			var conv struct {
				CurrentMessage struct {
					UserInputMessage struct {
						ModelID string `json:"modelId"`
					} `json:"userInputMessage"`
				} `json:"currentMessage"`
			}
			if err := json.Unmarshal(cs, &conv); err == nil {
				upstreamModelID = conv.CurrentMessage.UserInputMessage.ModelID
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(awsEventStreamFrame(t, "assistantResponseEvent", map[string]interface{}{
			"content": "mapped response",
		}))
	}))
	defer server.Close()

	oldEndpoints := kiroEndpoints
	kiroEndpoints = []kiroEndpoint{{URL: server.URL, Origin: "AI_EDITOR", Name: "test"}}
	defer func() { kiroEndpoints = oldEndpoints }()

	oldClient := kiroHttpStore.Load()
	kiroHttpStore.Store(&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}})
	defer kiroHttpStore.Store(oldClient)

	p := accountpool.GetPool()
	p.Reload()
	h := &Handler{
		pool:        p,
		promptCache: newPromptCacheTracker(defaultPromptCacheTTL),
	}

	body := map[string]interface{}{
		"model":      "claude-fable-5",
		"max_tokens": 100,
		"messages":   []map[string]interface{}{{"role": "user", "content": "hi"}},
		"stream":     false,
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// Auth bypass: the entry point calls authenticateForClaude before
	// handleClaudeMessagesInternal. We invoke the internal handler directly to
	// keep the test focused on the mapping behavior.
	h.handleClaudeMessagesInternal(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	if upstreamModelID != "gpt-5.6-sol" {
		t.Fatalf("expected upstream modelId = gpt-5.6-sol, got %q", upstreamModelID)
	}

	var resp ClaudeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Model != "claude-fable-5" {
		t.Fatalf("expected response model to echo facing claude-fable-5, got %q", resp.Model)
	}
}

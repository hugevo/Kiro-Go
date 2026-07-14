package proxy

import (
	"encoding/json"
	"kiro-go/auth"
	"kiro-go/config"
	"kiro-go/logger"
	"net/http"
	"strings"
)

// ApiKeyImportResult is the per-key result returned by the batch API key import.
type ApiKeyImportResult struct {
	Key     string `json:"key"`               // The key (truncated for safety)
	Success bool   `json:"success"`           // Whether the account was added
	Error   string `json:"error,omitempty"`   // Error message if not added
	Account *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"account,omitempty"` // Account details if added
}

// apiImportApiKeys handles POST /admin/api/auth/apikeys-batch.
// Body: { "keys": "one per line", "region": "us-east-1" }
// Each key is added as a separate account with AuthMethod "api_key".
// Duplicate keys (against existing accounts) are skipped.
func (h *Handler) apiImportApiKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys   string `json:"keys"`
		Region string `json:"region"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid JSON"})
		return
	}

	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = "us-east-1"
	}

	// Split keys by newline, trim whitespace, deduplicate within the batch.
	rawLines := strings.Split(req.Keys, "\n")
	seen := make(map[string]bool)
	var keys []string
	for _, line := range rawLines {
		key := strings.TrimSpace(line)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}

	if len(keys) == 0 {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "No API keys provided"})
		return
	}

	results := make([]ApiKeyImportResult, 0, len(keys))
	var addedAccounts []config.Account

	for _, key := range keys {
		// Deduplicate against existing accounts.
		if config.KiroApiKeyExists(key) {
			results = append(results, ApiKeyImportResult{
				Key:     maskApiKey(key),
				Success: false,
				Error:   "duplicate",
			})
			continue
		}

		account := config.Account{
			ID:          auth.GenerateAccountID(),
			AccessToken: key, // mirror so pool/dispatch reads AccessToken unchanged
			KiroApiKey:  key,
			AuthMethod:  "api_key",
			Region:      region,
			ExpiresAt:   0, // never refresh
			Enabled:     true,
			MachineId:   config.GenerateMachineId(),
		}

		if err := config.AddAccount(account); err != nil {
			results = append(results, ApiKeyImportResult{
				Key:     maskApiKey(key),
				Success: false,
				Error:   err.Error(),
			})
			continue
		}

		addedAccounts = append(addedAccounts, account)
		result := ApiKeyImportResult{
			Key:     maskApiKey(key),
			Success: true,
			Account: &struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			}{
				ID:    account.ID,
				Email: account.Email,
			},
		}
		results = append(results, result)
	}

	h.pool.Reload()

	// Best-effort refresh of account info and model cache for each added account.
	for _, acc := range addedAccounts {
		go func(a config.Account) {
			info, err := RefreshAccountInfo(&a)
			if err != nil {
				logger.Warnf("[ApiKeyBatch] RefreshAccountInfo failed for %s: %v", a.ID, err)
				return
			}
			_ = config.UpdateAccountInfo(a.ID, *info)
			h.pool.Reload()
			if fetchErr := h.fetchAndCacheAccountModels(&a); fetchErr != nil {
				logger.Warnf("[ApiKeyBatch] fetchAndCacheAccountModels failed for %s: %v", a.ID, fetchErr)
			}
		}(acc)
	}

	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Success {
			successCount++
		} else {
			failCount++
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"added":   successCount,
		"skipped": failCount,
		"results": results,
	})
}

// maskApiKey returns a truncated version of the API key for safe display in logs
// and responses. Shows the first 4 and last 4 characters with "***" in between.
func maskApiKey(key string) string {
	if len(key) <= 12 {
		return key[:2] + "***"
	}
	return key[:4] + "***" + key[len(key)-4:]
}

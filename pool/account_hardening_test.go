package pool

import (
	"kiro-go/config"
	"testing"
	"time"
)

// healthPool builds a pool with all health/circuit maps initialised, for
// hardening tests that exercise RecordError/RecordSuccess/RecordLatency.
func healthPool(accounts ...config.Account) *AccountPool {
	p := &AccountPool{
		cooldowns:      make(map[string]time.Time),
		errorCounts:    make(map[string]int),
		modelLists:     make(map[string]map[string]bool),
		circuitState:   make(map[string]*circuitBreaker),
		healthStats:    make(map[string]*accountHealth),
		apiKeyAffinity: make(map[string]apiKeyBinding),
	}
	p.accounts = accounts
	return p
}

// TestErrorRateDecaysOnSuccessNotReset verifies that a burst of errors followed
// by a single success leaves the account's health score still penalised — the
// error history must decay gradually, not reset to a clean slate on one success.
func TestErrorRateDecaysOnSuccessNotReset(t *testing.T) {
	p := healthPool()

	for i := 0; i < 5; i++ {
		p.RecordError("a", false)
	}
	p.RecordSuccess("a")

	score := p.healthScore("a", 1)
	if score >= 1.0 {
		t.Fatalf("expected decayed penalty < 1.0 after 5 errors + 1 success, got %v", score)
	}
	if score <= 0 {
		t.Fatalf("expected a positive (still-selectable) score, got %v", score)
	}
}

// TestCircuitHalfOpenPersistsViaSelection verifies that when the open window has
// elapsed, the *selection* path (not just the affinity path) persists the
// open->half-open transition, so the breaker actually enforces single-probe
// semantics instead of silently un-blocking every request.
func TestCircuitHalfOpenPersistsViaSelection(t *testing.T) {
	p := healthPool(config.Account{ID: "a", Enabled: true})

	cb := &circuitBreaker{
		state:          circuitOpen,
		consecutiveErr: circuitErrorThreshold,
		openedAt:       time.Now().Add(-circuitOpenDuration - time.Second),
	}
	p.circuitState["a"] = cb

	// Selection evaluates 'a': the open window has elapsed, so it should be
	// allowed through as a probe AND its state should be persisted as half-open.
	if acc := p.GetNextForModelExcluding("", nil); acc == nil || acc.ID != "a" {
		t.Fatalf("expected 'a' to be selectable as a probe, got %#v", acc)
	}
	if cb.state != circuitHalfOpen {
		t.Fatalf("expected circuit state half-open (%d) persisted via selection, got %d", circuitHalfOpen, cb.state)
	}
}

// TestRecordLatencyExportedInfluencesScore verifies the now-exported
// RecordLatency feeds the EWMA so a faster account outscores a slower one
// (the signal handlers are expected to supply).
func TestRecordLatencyExportedInfluencesScore(t *testing.T) {
	p := healthPool()
	p.RecordSuccess("fast")
	p.RecordLatency("fast", 50)
	p.RecordSuccess("slow")
	p.RecordLatency("slow", 8000)

	fast := p.healthScore("fast", 1)
	slow := p.healthScore("slow", 1)
	if fast <= slow {
		t.Fatalf("expected faster account to score higher: fast=%v slow=%v", fast, slow)
	}
}

// TestPruneExpiredAffinityRemovesStaleBindings verifies the affinity map is
// bounded: bindings older than the TTL are swept while fresh ones survive.
func TestPruneExpiredAffinityRemovesStaleBindings(t *testing.T) {
	p := healthPool()
	now := time.Now()
	p.apiKeyAffinity["fresh"] = apiKeyBinding{accountID: "a", lastUsed: now}
	p.apiKeyAffinity["stale"] = apiKeyBinding{accountID: "b", lastUsed: now.Add(-2 * sessionAffinityTTL)}

	p.mu.Lock()
	p.pruneExpiredAffinityLocked(now)
	p.mu.Unlock()

	if _, ok := p.apiKeyAffinity["stale"]; ok {
		t.Fatal("expected expired binding to be pruned")
	}
	if _, ok := p.apiKeyAffinity["fresh"]; !ok {
		t.Fatal("expected fresh binding to be retained")
	}
}

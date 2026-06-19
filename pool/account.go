// Package pool manages the account pool with circuit-breaking, EWMA latency
// tracking, weighted selection, and concurrency control.
package pool

import (
	"context"
	"errors"
	"kiro-go/config"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	tokenRefreshSkewSeconds int64   = 120
	ewmaAlpha               float64 = 0.2
)

// AccountState wraps a config.Account with runtime state: circuit breaker,
// EWMA latency, and usage counters.
type AccountState struct {
	Account      config.Account
	breaker      CircuitBreaker
	ewmaLatency  float64 // nanoseconds
	successCount uint64
	errorCount   uint64
	lastUsed     time.Time
}

func (s *AccountState) recordLatency(latency time.Duration) {
	if s.ewmaLatency == 0 {
		s.ewmaLatency = float64(latency)
	} else {
		s.ewmaLatency = ewmaAlpha*float64(latency) + (1-ewmaAlpha)*s.ewmaLatency
	}
}

func (s *AccountState) effectiveWeight(targetLatency float64) int {
	base := effectiveWeight(s.Account.Weight)
	if s.ewmaLatency == 0 || targetLatency == 0 {
		return base
	}
	ratio := targetLatency / s.ewmaLatency
	w := int(float64(base) * ratio)
	if w < 1 {
		w = 1
	}
	if maxW := base * 3; w > maxW {
		w = maxW
	}
	return w
}

// newBreaker creates a circuit breaker with defaults suitable for the pool.
func newBreaker() CircuitBreaker {
	return *NewCircuitBreaker(3, 30*time.Second, 300*time.Second, 3600*time.Second)
}

// AccountPool manages a set of accounts with weighted round-robin dispatch.
type AccountPool struct {
	mu            sync.RWMutex
	states        []*AccountState
	totalAccounts int
	currentIndex  uint64
	modelLists    map[string]map[string]bool // accountID → set of modelIDs
	sem           chan struct{}
	queueTimeout  time.Duration
}

var (
	pool     *AccountPool
	poolOnce sync.Once
)

// GetPool returns the global account pool singleton.
func GetPool() *AccountPool {
	poolOnce.Do(func() {
		pool = &AccountPool{
			modelLists:   make(map[string]map[string]bool),
			queueTimeout: 30 * time.Second,
		}
		pool.Reload()
	})
	return pool
}

// Reload rebuilds the state list from config, preserving existing AccountState
// wrappers so circuit breaker and EWMA data survive across reloads.
func (p *AccountPool) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()

	enabled := config.GetEnabledAccounts()
	allowOverUsage := config.GetAllowOverUsage()

	existing := make(map[string]*AccountState)
	for _, st := range p.states {
		existing[st.Account.ID] = st
	}

	var newStates []*AccountState
	for _, a := range enabled {
		if isQuotaBlocked(a, allowOverUsage) {
			continue
		}
		if st, ok := existing[a.ID]; ok {
			st.Account = a
			newStates = append(newStates, st)
		} else {
			newStates = append(newStates, &AccountState{
				Account: a,
				breaker: newBreaker(),
			})
		}
	}
	p.states = newStates
	p.totalAccounts = len(enabled)
	p.SetMaxConcurrent(len(newStates) * 3)
}

// ---------------------------------------------------------------------------
// Concurrency control (semaphore)
// ---------------------------------------------------------------------------

// SetMaxConcurrent configures the concurrency limit. n <= 0 auto-computes
// len(states) × 3. The semaphore is re-created only when the capacity changes.
func (p *AccountPool) SetMaxConcurrent(n int) {
	if n <= 0 {
		n = len(p.states) * 3
	}
	if n < 1 {
		n = 1
	}
	if p.sem == nil || cap(p.sem) != n {
		p.sem = make(chan struct{}, n)
	}
}

// Acquire blocks until a concurrency slot is available or ctx expires.
func (p *AccountPool) Acquire(ctx context.Context) error {
	p.mu.RLock()
	sem := p.sem
	p.mu.RUnlock()
	if sem == nil {
		return nil
	}
	select {
	case sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a concurrency slot to the pool.
func (p *AccountPool) Release() {
	p.mu.RLock()
	sem := p.sem
	p.mu.RUnlock()
	if sem == nil {
		return
	}
	select {
	case <-sem:
	default:
	}
}

// QueueDepth returns the number of currently occupied concurrency slots.
func (p *AccountPool) QueueDepth() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.sem == nil {
		return 0
	}
	return len(p.sem)
}

// MaxConcurrent returns the total concurrency capacity.
func (p *AccountPool) MaxConcurrent() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.sem == nil {
		return 0
	}
	return cap(p.sem)
}

// ---------------------------------------------------------------------------
// Account selection
// ---------------------------------------------------------------------------

// GetNext returns the next available account using weighted selection.
func (p *AccountPool) GetNext() *config.Account {
	return p.GetNextExcluding(nil)
}

// GetNextExcluding returns the next available account, skipping those in excluded.
func (p *AccountPool) GetNextExcluding(excluded map[string]bool) *config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.states) == 0 {
		return nil
	}

	allowOverUsage := config.GetAllowOverUsage()
	now := time.Now()

	var candidates []*AccountState
	targetLatency := p.ewmaTargetLatency()

	for _, st := range p.states {
		if excluded != nil && excluded[st.Account.ID] {
			continue
		}
		if !st.breaker.CanRoute() {
			continue
		}
		if st.Account.ExpiresAt > 0 && now.Unix() > st.Account.ExpiresAt-tokenRefreshSkewSeconds {
			continue
		}
		if isQuotaBlocked(st.Account, allowOverUsage) {
			continue
		}
		candidates = append(candidates, st)
	}

	if len(candidates) > 0 {
		return p.selectWeighted(candidates, targetLatency)
	}

	// Fallback: pick any HALF_OPEN account
	for _, st := range p.states {
		if excluded != nil && excluded[st.Account.ID] {
			continue
		}
		if st.breaker.StateString() == "HALF_OPEN" {
			return &st.Account
		}
	}
	return nil
}

// selectWeighted picks one account from candidates using their effective weights.
func (p *AccountPool) selectWeighted(candidates []*AccountState, targetLatency float64) *config.Account {
	if len(candidates) == 1 {
		return &candidates[0].Account
	}

	totalWeight := 0
	weights := make([]int, len(candidates))
	for i, st := range candidates {
		w := st.effectiveWeight(targetLatency)
		totalWeight += w
		weights[i] = w
	}

	if totalWeight == 0 {
		return &candidates[0].Account
	}

	idx := int(atomic.AddUint64(&p.currentIndex, 1) % uint64(totalWeight))
	cumulative := 0
	for i, w := range weights {
		cumulative += w
		if idx < cumulative {
			return &candidates[i].Account
		}
	}
	return &candidates[len(candidates)-1].Account
}

// ewmaTargetLatency computes the average EWMA latency across all accounts.
func (p *AccountPool) ewmaTargetLatency() float64 {
	var sum float64
	count := 0
	for _, st := range p.states {
		if st.ewmaLatency > 0 {
			sum += st.ewmaLatency
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return sum / float64(count)
}

// ---------------------------------------------------------------------------
// Model-aware selection
// ---------------------------------------------------------------------------

// GetNextForModel returns the next available account supporting the given model.
func (p *AccountPool) GetNextForModel(model string) *config.Account {
	return p.GetNextForModelExcluding(model, nil)
}

// GetNextForModelExcluding returns the next available account supporting the
// given model, skipping those in excluded.
func (p *AccountPool) GetNextForModelExcluding(model string, excluded map[string]bool) *config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.states) == 0 {
		return nil
	}

	allowOverUsage := config.GetAllowOverUsage()
	now := time.Now()
	targetLatency := p.ewmaTargetLatency()

	var candidates []*AccountState
	for _, st := range p.states {
		if excluded != nil && excluded[st.Account.ID] {
			continue
		}
		if !st.breaker.CanRoute() {
			continue
		}
		if !p.accountHasModel(st.Account.ID, model) {
			continue
		}
		if st.Account.ExpiresAt > 0 && now.Unix() > st.Account.ExpiresAt-tokenRefreshSkewSeconds {
			continue
		}
		if isQuotaBlocked(st.Account, allowOverUsage) {
			continue
		}
		candidates = append(candidates, st)
	}

	if len(candidates) > 0 {
		return p.selectWeighted(candidates, targetLatency)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Model list cache
// ---------------------------------------------------------------------------

// SetModelList caches the set of model IDs supported by an account.
func (p *AccountPool) SetModelList(accountID string, modelIDs []string) {
	set := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		set[strings.ToLower(strings.TrimSpace(id))] = true
	}
	p.mu.Lock()
	p.modelLists[accountID] = set
	p.mu.Unlock()
}

// GetModelList returns the cached model IDs for an account.
func (p *AccountPool) GetModelList(accountID string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	set, ok := p.modelLists[accountID]
	if !ok || len(set) == 0 {
		return []string{}
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
}

// accountHasModel checks whether an account supports a given model.
// Returns true if the model list is not yet loaded (cold start).
func (p *AccountPool) accountHasModel(accountID, model string) bool {
	list, ok := p.modelLists[accountID]
	if !ok || len(list) == 0 {
		return true
	}
	return list[strings.ToLower(strings.TrimSpace(model))]
}

// ---------------------------------------------------------------------------
// Lookup + lifecycle
// ---------------------------------------------------------------------------

// GetByID finds an account by ID in the pool.
func (p *AccountPool) GetByID(id string) *config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, st := range p.states {
		if st.Account.ID == id {
			return &st.Account
		}
	}
	return nil
}

// RecordSuccess clears the breaker for a successful request and updates EWMA.
func (p *AccountPool) RecordSuccess(id string, latency time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.states {
		if st.Account.ID == id {
			st.breaker.Transition(nil, "")
			st.recordLatency(latency)
			atomic.AddUint64(&st.successCount, 1)
			st.lastUsed = time.Now()
			return
		}
	}
}

// RecordError records a failed request and may open the circuit breaker.
func (p *AccountPool) RecordError(id string, err error, isQuotaError bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.states {
		if st.Account.ID == id {
			errorType := "transient"
			if IsAuthFailure(err) {
				errorType = "auth"
			} else if isQuotaError {
				errorType = "quota"
			}
			st.breaker.Transition(err, errorType)
			atomic.AddUint64(&st.errorCount, 1)
			return
		}
	}
}

// MarkOverLimit puts an account into quota cooldown and reloads.
func (p *AccountPool) MarkOverLimit(id string) {
	p.mu.Lock()
	for _, st := range p.states {
		if st.Account.ID == id {
			st.breaker.Transition(errors.New("over limit"), "quota")
		}
	}
	p.mu.Unlock()
	p.Reload()
}

// DisableAccount marks an account as disabled and reloads the pool.
func (p *AccountPool) DisableAccount(id, reason string) {
	if err := config.SetAccountBanStatus(id, "DISABLED", reason); err != nil {
		_ = err
	}
	p.mu.Lock()
	for _, st := range p.states {
		if st.Account.ID == id {
			st.breaker.Transition(errors.New("account disabled"), "auth")
		}
	}
	p.mu.Unlock()
	p.Reload()
}

// UpdateToken refreshes the access/refresh token for an account in-memory.
func (p *AccountPool) UpdateToken(id, accessToken, refreshToken string, expiresAt int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, st := range p.states {
		if st.Account.ID == id {
			st.Account.AccessToken = accessToken
			if refreshToken != "" {
				st.Account.RefreshToken = refreshToken
			}
			st.Account.ExpiresAt = expiresAt
		}
	}
}

// Count returns total number of unique accounts (including disabled).
func (p *AccountPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.totalAccounts > 0 {
		return p.totalAccounts
	}
	seen := make(map[string]bool)
	for _, st := range p.states {
		seen[st.Account.ID] = true
	}
	return len(seen)
}

// AvailableCount returns the number of accounts currently routable.
func (p *AccountPool) AvailableCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	count := 0
	seen := make(map[string]bool)
	for _, st := range p.states {
		if seen[st.Account.ID] {
			continue
		}
		seen[st.Account.ID] = true
		if st.breaker.CanRoute() {
			count++
		}
	}
	return count
}

// UpdateStats increments per-account request and token counters.
func (p *AccountPool) UpdateStats(id string, tokens int, credits float64) {
	p.mu.Lock()
	var requestCount, errorCount, totalTokens int
	var totalCredits float64
	var lastUsed int64
	var updated bool
	for _, st := range p.states {
		if st.Account.ID == id {
			if !updated {
				st.Account.RequestCount++
				st.Account.TotalTokens += tokens
				st.Account.TotalCredits += credits
				st.Account.LastUsed = time.Now().Unix()
				requestCount = st.Account.RequestCount
				errorCount = int(atomic.LoadUint64(&st.errorCount))
				totalTokens = st.Account.TotalTokens
				totalCredits = st.Account.TotalCredits
				lastUsed = st.Account.LastUsed
				updated = true
				continue
			}
			st.Account.RequestCount = requestCount
			st.Account.ErrorCount = errorCount
			st.Account.TotalTokens = totalTokens
			st.Account.TotalCredits = totalCredits
			st.Account.LastUsed = lastUsed
		}
	}
	p.mu.Unlock()
	if updated {
		go config.UpdateAccountStats(id, requestCount, errorCount, totalTokens, totalCredits, lastUsed)
	}
}

// GetAllAccounts returns a snapshot of all account configs in the pool.
func (p *AccountPool) GetAllAccounts() []config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]config.Account, len(p.states))
	for i, st := range p.states {
		result[i] = st.Account
	}
	return result
}

// GetStatus returns runtime status for all accounts (admin API).
func (p *AccountPool) GetStatus() []map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	target := p.ewmaTargetLatency()
	result := make([]map[string]interface{}, len(p.states))
	for i, st := range p.states {
		result[i] = map[string]interface{}{
			"id":              st.Account.ID,
			"state":           st.breaker.StateString(),
			"ewmaLatency":     time.Duration(st.ewmaLatency).String(),
			"effectiveWeight": st.effectiveWeight(target),
			"successes":       atomic.LoadUint64(&st.successCount),
			"errors":          atomic.LoadUint64(&st.errorCount),
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Error classification (unchanged)
// ---------------------------------------------------------------------------

// IsAuthFailure reports whether an error indicates revoked credentials.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if hasStatusToken(msg, "401") || hasStatusToken(msg, "403") {
		return true
	}
	if strings.Contains(lower, "bad credentials") ||
		strings.Contains(lower, "invalid_grant") ||
		strings.Contains(lower, "invalid grant") ||
		strings.Contains(lower, "invalid_token") ||
		strings.Contains(lower, "invalid token") ||
		strings.Contains(lower, "token expired") ||
		strings.Contains(lower, "token has expired") ||
		strings.Contains(lower, "unauthorized") {
		return true
	}
	return false
}

func hasStatusToken(s, status string) bool {
	for {
		idx := strings.Index(s, status)
		if idx < 0 {
			return false
		}
		leftOK := idx == 0 || !isDigit(s[idx-1])
		rightIdx := idx + len(status)
		rightOK := rightIdx >= len(s) || !isDigit(s[rightIdx])
		if leftOK && rightOK {
			return true
		}
		s = s[idx+len(status):]
	}
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// IsSuspensionError reports whether the error indicates the account is suspended.
func IsSuspensionError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "temporarily_suspended") ||
		strings.Contains(lower, "temporarily suspended") ||
		strings.Contains(lower, "no available kiro profile")
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func isOverUsageLimit(acc config.Account) bool {
	return acc.UsageLimit > 0 && acc.UsageCurrent >= acc.UsageLimit
}

func isQuotaBlocked(acc config.Account, allowOverUsage bool) bool {
	return isOverUsageLimit(acc) && !isUpstreamOverageEnabled(acc) && !allowOverUsage
}

func isUpstreamOverageEnabled(acc config.Account) bool {
	return strings.EqualFold(acc.OverageStatus, "ENABLED")
}

func effectiveWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	return weight
}

// Package pool 账号池管理
// 实现轮询负载均衡、错误冷却、Token 刷新
package pool

import (
	"kiro-go/auth"
	"kiro-go/config"
	"math/rand"
	"strings"
	"sync"
	"time"
)

const tokenRefreshSkewSeconds int64 = 120

const (
	circuitClosed          = 0
	circuitOpen            = 1
	circuitHalfOpen        = 2
	circuitErrorThreshold  = 5
	circuitOpenDuration    = 30 * time.Second
)

type circuitBreaker struct {
	state          int
	consecutiveErr int
	openedAt       time.Time
}

// accountHealth tracks per-account health signals used by score-weighted
// selection: EWMA latency plus success/error counts.
type accountHealth struct {
	ewmaLatencyMs float64 // EWMA latency (α=0.3)
	successCount  int
	errorCount    int
}

// isCircuitOpen reports whether an account's circuit breaker is currently open
// (and should be skipped). It also transitions open→half-open after the open
// duration elapses, allowing a single probe request through.
func (p *AccountPool) isCircuitOpen(id string, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	cb, ok := p.circuitState[id]
	if !ok || cb == nil {
		return false
	}
	switch cb.state {
	case circuitOpen:
		if now.Sub(cb.openedAt) >= circuitOpenDuration {
			cb.state = circuitHalfOpen // transition to half-open after timeout
			return false               // allow one probe
		}
		return true
	case circuitHalfOpen:
		return false // allow the probe through
	default:
		return false
	}
}

// isCircuitOpenLocked is a lock-free variant for use while the caller already
// holds p.mu (e.g. inside GetNextForModelExcluding, which holds the RLock).
// Calling p.mu.Lock() from inside a held RLock deadlocks, so selection paths
// use this read-only check instead. The open→half-open transition happens via
// isCircuitOpen (external callers); here we simply allow a probe through once
// the open duration has elapsed.
func (p *AccountPool) isCircuitOpenLocked(id string, now time.Time) bool {
	cb, ok := p.circuitState[id]
	if !ok || cb == nil {
		return false
	}
	switch cb.state {
	case circuitOpen:
		if now.Sub(cb.openedAt) >= circuitOpenDuration {
			return false // half-open transition
		}
		return true
	default:
		return false
	}
}

// AccountPool 账号池
type AccountPool struct {
	mu             sync.RWMutex
	accounts       []config.Account
	totalAccounts  int
	currentIndex   uint64
	cooldowns      map[string]time.Time       // 账号冷却时间
	errorCounts    map[string]int             // 连续错误计数
	modelLists     map[string]map[string]bool // accountID → set of modelIDs (from ListAvailableModels)
	reprobeBackoff map[string]time.Duration   // accountID → next backoff interval
	reprobeNext    map[string]time.Time       // accountID → when to next probe
	stopRecover    chan struct{}
	circuitState   map[string]*circuitBreaker // accountID → circuit breaker state
	healthStats    map[string]*accountHealth  // accountID → EWMA latency + error/success counts
}

var (
	pool     *AccountPool
	poolOnce sync.Once
)

// GetPool 获取全局账号池单例
func GetPool() *AccountPool {
	poolOnce.Do(func() {
		pool = &AccountPool{
			cooldowns:      make(map[string]time.Time),
			errorCounts:    make(map[string]int),
			modelLists:     make(map[string]map[string]bool),
			reprobeBackoff: make(map[string]time.Duration),
			reprobeNext:    make(map[string]time.Time),
			circuitState:   make(map[string]*circuitBreaker),
			healthStats:    make(map[string]*accountHealth),
		}
		pool.Reload()
		if config.GetAutoRecoverEnabled() {
			pool.startAutoRecover()
		}
	})
	return pool
}

// Reload rebuilds the account list from config (one entry per account; weight is
// handled as selection probability by healthScore, not as duplicated slots).
// Over-quota accounts are dropped unless either the per-account upstream
// Overages switch (OverageStatus=ENABLED) or the global AllowOverUsage
// setting permits over-quota routing.
func (p *AccountPool) Reload() {
	p.mu.Lock()
	defer p.mu.Unlock()
	enabled := config.GetEnabledAccounts()
	allowOverUsage := config.GetAllowOverUsage()
	var accounts []config.Account
	for _, a := range enabled {
		if isQuotaBlocked(a, allowOverUsage) {
			continue
		}
		accounts = append(accounts, a) // one entry per account (weight handled by score)
	}
	p.accounts = accounts
	p.totalAccounts = len(enabled)
}

// GetNext 获取下一个可用账号（加权轮询）
func (p *AccountPool) GetNext() *config.Account {
	return p.GetNextExcluding(nil)
}

// GetNextExcluding 获取下一个可用账号（健康加权随机），并跳过指定账号。
// Delegates to GetNextForModelExcluding with model="" (any model).
func (p *AccountPool) GetNextExcluding(excluded map[string]bool) *config.Account {
	return p.GetNextForModelExcluding("", excluded)
}

// SetModelList 缓存账号支持的模型集合（由 handler 在刷新后调用）
func (p *AccountPool) SetModelList(accountID string, modelIDs []string) {
	set := make(map[string]bool, len(modelIDs))
	for _, id := range modelIDs {
		set[strings.ToLower(strings.TrimSpace(id))] = true
	}
	p.mu.Lock()
	p.modelLists[accountID] = set
	p.mu.Unlock()
}

// GetModelList 返回该账号缓存的模型 ID 列表（供 admin API 使用）。
// 若尚无缓存则返回空切片。
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

// accountHasModel 检查账号是否支持指定模型。
// 若该账号尚无模型列表（冷启动），视为支持所有模型。
func (p *AccountPool) accountHasModel(accountID, model string) bool {
	list, ok := p.modelLists[accountID]
	if !ok || len(list) == 0 {
		return true // 冷启动：列表未就绪，乐观放行
	}
	return list[strings.ToLower(strings.TrimSpace(model))]
}

// GetNextForModel 获取下一个支持指定模型的可用账号。
// model 应为去掉 thinking 后缀的实际模型名。
// 若无账号有该模型列表数据，行为与 GetNext 相同（乐观路由）。
func (p *AccountPool) GetNextForModel(model string) *config.Account {
	return p.GetNextForModelExcluding(model, nil)
}

// GetNextForModelExcluding selects an account supporting the model using
// health-aware, score-weighted random selection (weight = probability, not
// slot count). Skips excluded, cooled-down, circuit-open, token-expiring, and
// quota-blocked accounts. model="" means "any model".
func (p *AccountPool) GetNextForModelExcluding(model string, excluded map[string]bool) *config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.accounts) == 0 {
		return nil
	}

	allowOverUsage := config.GetAllowOverUsage()
	now := time.Now()

	// Build candidate list with health scores.
	type candidate struct {
		acc   *config.Account
		score float64
	}
	var candidates []candidate
	totalScore := 0.0
	for i := range p.accounts {
		acc := &p.accounts[i]
		if excluded != nil && excluded[acc.ID] {
			continue
		}
		if model != "" && !p.accountHasModel(acc.ID, model) {
			continue
		}
		if cooldown, ok := p.cooldowns[acc.ID]; ok && now.Before(cooldown) {
			continue
		}
		if p.isCircuitOpenLocked(acc.ID, now) {
			continue
		}
		if acc.ExpiresAt > 0 && now.Unix() > acc.ExpiresAt-tokenRefreshSkewSeconds {
			continue
		}
		if isQuotaBlocked(*acc, allowOverUsage) {
			continue
		}
		score := p.healthScore(acc.ID, effectiveWeight(acc.Weight))
		if score <= 0 {
			score = 0.01 // tiny non-zero so a degraded account is still selectable as fallback
		}
		candidates = append(candidates, candidate{acc, score})
		totalScore += score
	}

	if len(candidates) == 0 {
		// Fallback: return the account with the earliest cooldown.
		return p.fallbackEarliestCooldown(model, excluded, allowOverUsage)
	}

	// Score-weighted random selection.
	r := rand.Float64() * totalScore
	cumulative := 0.0
	for _, c := range candidates {
		cumulative += c.score
		if r <= cumulative {
			return c.acc
		}
	}
	return candidates[len(candidates)-1].acc
}

// GetByID 根据 ID 获取账号
func (p *AccountPool) GetByID(id string) *config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			return &p.accounts[i]
		}
	}
	return nil
}

// RecordSuccess 记录请求成功，清除冷却
func (p *AccountPool) RecordSuccess(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cooldowns, id)
	p.errorCounts[id] = 0
	// Circuit breaker: reset on success.
	if cb, ok := p.circuitState[id]; ok && cb != nil {
		cb.state = circuitClosed
		cb.consecutiveErr = 0
	}
	// Health stats: record success.
	if h, ok := p.healthStats[id]; ok && h != nil {
		h.successCount++
		h.errorCount = 0
	}
}

// RecordError 记录请求错误，设置冷却
func (p *AccountPool) RecordError(id string, isQuotaError bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.errorCounts[id]++

	if isQuotaError {
		// 配额错误，冷却 1 小时
		p.cooldowns[id] = time.Now().Add(time.Hour)
	} else if p.errorCounts[id] >= 3 {
		// 连续 3 次错误，冷却 1 分钟
		p.cooldowns[id] = time.Now().Add(time.Minute)
	}

	// Circuit breaker: track consecutive errors.
	cb := p.circuitState[id]
	if cb == nil {
		cb = &circuitBreaker{state: circuitClosed}
		p.circuitState[id] = cb
	}
	cb.consecutiveErr++
	if cb.state == circuitHalfOpen {
		// Probe failed → re-open.
		cb.state = circuitOpen
		cb.openedAt = time.Now()
	} else if cb.consecutiveErr >= circuitErrorThreshold && cb.state == circuitClosed {
		cb.state = circuitOpen
		cb.openedAt = time.Now()
	}
	// Health stats: record error (create entry so errorRate is accurate).
	// Guard against nil healthStats (test pools may leave it uninitialized).
	if p.healthStats != nil {
		h := p.healthStats[id]
		if h == nil {
			h = &accountHealth{}
			p.healthStats[id] = h
		}
		h.errorCount++
	}
}

// IsAuthFailure reports whether an error indicates the refresh token / credentials
// have been revoked or invalidated upstream (401, 403 with auth markers, etc.).
// These accounts cannot be recovered automatically and must be re-authenticated.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)

	// Match HTTP status codes only when they appear as standalone tokens to avoid
	// false positives from arbitrary digits in the error body (e.g. request IDs).
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

// hasStatusToken returns true when status appears in s with non-digit boundaries
// on both sides, so "401" matches "HTTP 401 from ..." but not "request_401abc".
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

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// IsSuspensionError reports whether the error indicates the account has been
// temporarily suspended by upstream or has no available Kiro profile.
// Unlike auth failures (revoked credentials), these may be transient, but
// the account should be disabled until an operator re-enables it.
func IsSuspensionError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "temporarily_suspended") ||
		strings.Contains(lower, "temporarily suspended") ||
		strings.Contains(lower, "no available kiro profile")
}

// DisableAccount marks an account as disabled (auth revoked / unrecoverable),
// removes it from the in-memory pool so subsequent requests skip it, and
// persists the change via config.SetAccountBanStatus.
func (p *AccountPool) DisableAccount(id, reason string) {
	if err := config.SetAccountBanStatus(id, "DISABLED", reason); err != nil {
		// best effort — even if persistence fails, drop it from memory
		_ = err
	}
	p.mu.Lock()
	// Long cooldown as a safety net in case Reload races
	p.cooldowns[id] = time.Now().Add(24 * time.Hour)
	p.mu.Unlock()
	p.Reload()
}

// MarkOverLimit marks an account as over usage limit (after a 402 / OVERAGE response).
// With the upstream OverageStatus model, the live status is refreshed via
// FetchOverageStatus from the request handler; here we just cooldown briefly so
// the next attempt picks a different account, then reload.
func (p *AccountPool) MarkOverLimit(id string) {
	p.mu.Lock()
	p.cooldowns[id] = time.Now().Add(time.Hour)
	p.mu.Unlock()
	p.Reload()
}

// UpdateToken 更新账号 Token
func (p *AccountPool) UpdateToken(id, accessToken, refreshToken string, expiresAt int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			p.accounts[i].AccessToken = accessToken
			if refreshToken != "" {
				p.accounts[i].RefreshToken = refreshToken
			}
			p.accounts[i].ExpiresAt = expiresAt
		}
	}
}

// Count 返回账号总数
func (p *AccountPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.totalAccounts > 0 {
		return p.totalAccounts
	}

	seen := make(map[string]bool)
	for _, acc := range p.accounts {
		seen[acc.ID] = true
	}
	return len(seen)
}

// AvailableCount 返回可用账号数
func (p *AccountPool) AvailableCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	count := 0
	seen := make(map[string]bool)
	for _, acc := range p.accounts {
		if seen[acc.ID] {
			continue
		}
		seen[acc.ID] = true
		if cooldown, ok := p.cooldowns[acc.ID]; ok && now.Before(cooldown) {
			continue
		}
		count++
	}
	return count
}

// UpdateStats 更新账号统计
func (p *AccountPool) UpdateStats(id string, tokens int, credits float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var updated bool
	var requestCount, errorCount, totalTokens int
	var totalCredits float64
	var lastUsed int64
	for i := range p.accounts {
		if p.accounts[i].ID == id {
			if !updated {
				p.accounts[i].RequestCount++
				p.accounts[i].TotalTokens += tokens
				p.accounts[i].TotalCredits += credits
				p.accounts[i].LastUsed = time.Now().Unix()

				requestCount = p.accounts[i].RequestCount
				errorCount = p.accounts[i].ErrorCount
				totalTokens = p.accounts[i].TotalTokens
				totalCredits = p.accounts[i].TotalCredits
				lastUsed = p.accounts[i].LastUsed
				updated = true
				continue
			}
			p.accounts[i].RequestCount = requestCount
			p.accounts[i].ErrorCount = errorCount
			p.accounts[i].TotalTokens = totalTokens
			p.accounts[i].TotalCredits = totalCredits
			p.accounts[i].LastUsed = lastUsed
		}
	}
	if updated {
		go config.UpdateAccountStats(id, requestCount, errorCount, totalTokens, totalCredits, lastUsed)
	}
}

// GetAllAccounts 获取所有账号副本
func (p *AccountPool) GetAllAccounts() []config.Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]config.Account, len(p.accounts))
	copy(result, p.accounts)
	return result
}

func isOverUsageLimit(acc config.Account) bool {
	return acc.UsageLimit > 0 && acc.UsageCurrent >= acc.UsageLimit
}

// isQuotaBlocked reports whether an over-quota account should be skipped:
// the per-account upstream Overages switch (OverageStatus=ENABLED) and the
// global allowOverUsage setting are the two ways to keep it routable.
func isQuotaBlocked(acc config.Account, allowOverUsage bool) bool {
	return isOverUsageLimit(acc) && !isUpstreamOverageEnabled(acc) && !allowOverUsage
}

// isUpstreamOverageEnabled reports whether the upstream Overages switch is ON for this account.
// "ENABLED" → true; anything else (DISABLED, UNKNOWN, empty) → false.
func isUpstreamOverageEnabled(acc config.Account) bool {
	return strings.EqualFold(acc.OverageStatus, "ENABLED")
}

func effectiveWeight(weight int) int {
	if weight < 1 {
		return 1
	}
	return weight
}

// recordLatency records an observed request latency into the account's EWMA
// (α=0.3). Called from request handlers after a response completes.
func (p *AccountPool) recordLatency(id string, latencyMs float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := p.healthStats[id]
	if h == nil {
		h = &accountHealth{}
		p.healthStats[id] = h
	}
	if h.ewmaLatencyMs == 0 {
		h.ewmaLatencyMs = latencyMs
	} else {
		h.ewmaLatencyMs = 0.3*latencyMs + 0.7*h.ewmaLatencyMs
	}
}

// healthScore returns a selection weight for the account: higher = preferred.
// score = effectiveWeight × (1 - errorRate) × (1 / (1 + latency/1000))
// Lock-free: the caller (GetNextForModelExcluding) already holds p.mu RLock.
func (p *AccountPool) healthScore(id string, weight int) float64 {
	w := float64(weight)
	if w < 1 {
		w = 1
	}
	h := p.healthStats[id]
	if h == nil {
		return w // no data → default weight
	}
	total := h.successCount + h.errorCount
	if total == 0 {
		return w
	}
	errorRate := float64(h.errorCount) / float64(total)
	latencyFactor := 1.0 / (1.0 + h.ewmaLatencyMs/1000.0)
	return w * (1.0 - errorRate) * latencyFactor
}

// fallbackEarliestCooldown returns the account with the earliest cooldown
// (or one with no cooldown at all) when no fully-healthy candidate exists.
// model="" means "any model". Caller must hold p.mu (at least RLock).
func (p *AccountPool) fallbackEarliestCooldown(model string, excluded map[string]bool, allowOverUsage bool) *config.Account {
	var best *config.Account
	var earliest time.Time
	for i := range p.accounts {
		acc := &p.accounts[i]
		if excluded != nil && excluded[acc.ID] {
			continue
		}
		if model != "" && !p.accountHasModel(acc.ID, model) {
			continue
		}
		if isQuotaBlocked(*acc, allowOverUsage) {
			continue
		}
		if cooldown, ok := p.cooldowns[acc.ID]; ok {
			if best == nil || cooldown.Before(earliest) {
				best = acc
				earliest = cooldown
			}
		} else {
			return acc
		}
	}
	return best
}

// startAutoRecover launches a background goroutine that periodically refreshes
// disabled accounts' tokens. If a refresh succeeds, the account is re-enabled.
// Exponential backoff per account: 1m → 5m → 30m → max 2h.
func (p *AccountPool) startAutoRecover() {
	p.stopRecover = make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.reprobeDisabled()
			case <-p.stopRecover:
				return
			}
		}
	}()
}

// reprobeDisabled iterates disabled accounts whose reprobe time has arrived,
// attempts a token refresh, and re-enables on success.
func (p *AccountPool) reprobeDisabled() {
	if !config.GetAutoRecoverEnabled() {
		return
	}
	now := time.Now()
	all := config.GetAccounts()
	for _, acc := range all {
		if acc.Enabled || acc.BanStatus != "DISABLED" {
			continue
		}
		// Check backoff schedule.
		p.mu.Lock()
		next, ok := p.reprobeNext[acc.ID]
		backoff := p.reprobeBackoff[acc.ID]
		p.mu.Unlock()
		if ok && now.Before(next) {
			continue // not time yet
		}
		// Attempt refresh.
		_, _, _, _, err := auth.RefreshToken(&acc)
		if err == nil {
			// Success! Re-enable.
			config.SetAccountEnabled(acc.ID, true)
			p.Reload()
			p.mu.Lock()
			delete(p.reprobeBackoff, acc.ID)
			delete(p.reprobeNext, acc.ID)
			p.mu.Unlock()
			continue
		}
		// Failure → increase backoff: 1m → 5m → 30m → 2h max.
		if backoff == 0 {
			backoff = time.Minute
		} else {
			backoff *= 5
			if backoff > 2*time.Hour {
				backoff = 2 * time.Hour
			}
		}
		p.mu.Lock()
		p.reprobeBackoff[acc.ID] = backoff
		p.reprobeNext[acc.ID] = now.Add(backoff)
		p.mu.Unlock()
	}
}

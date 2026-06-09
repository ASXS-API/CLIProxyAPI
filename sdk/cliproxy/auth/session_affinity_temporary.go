package auth

import (
	"fmt"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	sessionAffinityTemporaryAuthTTL         = 60 * time.Minute
	sessionAffinityTemporaryAuthMaxFailures = 5
	// sessionAffinityTemporaryAuthCleanupSweepInterval throttles the inline
	// expiry/prune sweep invoked from the hot per-request paths
	// (rememberLocal/markDeleted/get). The sweep is O(num_auths × sessions_per_auth)
	// and was previously run on every resolved request, dominating CPU under load.
	// The dedicated cleanupLoop goroutine (every ttl/2) provides the eventual-
	// reclamation guarantee, and pruneSessionFailuresLocked only drops counters
	// older than ttl, so bounding the inline sweep to this interval is behaviorally
	// invisible while collapsing thousands of redundant sweeps/sec into ~one.
	sessionAffinityTemporaryAuthCleanupSweepInterval = 30 * time.Second
)

// sessionFailureState tracks consecutive failures for a single session (cache
// key) against one temporary in-memory auth. Counting is per session so that a
// single misbehaving session never affects the credential shared by others.
type sessionFailureState struct {
	count         int
	lastRequestID string
	lastAt        time.Time
}

type sessionAffinityTemporaryAuthEntry struct {
	auth          *Auth
	expiresAt     time.Time
	deletedAt     time.Time
	hitCount      int
	successCount  int
	failureCount  int
	lastSuccessAt time.Time
	lastFailureAt time.Time
	// sessionFailures holds per-session consecutive-failure counters keyed by
	// the session cache key (provider::session::model). It is intentionally NOT
	// a single global counter: the temporary credential is shared by many sticky
	// sessions and must outlive any one session's failures.
	sessionFailures map[string]*sessionFailureState
}

type sessionAffinityTemporaryAuthStore struct {
	mu          sync.Mutex
	auths       map[string]*sessionAffinityTemporaryAuthEntry
	ttl         time.Duration
	maxFails    int
	stopCh      chan struct{}
	lastCleanup time.Time
}

func newSessionAffinityTemporaryAuthStore(ttl time.Duration, maxFails int) *sessionAffinityTemporaryAuthStore {
	if ttl <= 0 {
		ttl = sessionAffinityTemporaryAuthTTL
	}
	if maxFails <= 0 {
		maxFails = sessionAffinityTemporaryAuthMaxFailures
	}
	s := &sessionAffinityTemporaryAuthStore{
		auths:    make(map[string]*sessionAffinityTemporaryAuthEntry),
		ttl:      ttl,
		maxFails: maxFails,
		stopCh:   make(chan struct{}),
	}
	// A dedicated sweeper guarantees that a credential abandoned by all sessions
	// is reclaimed once its TTL elapses, rather than depending on unrelated auth
	// add/remove/access events to trigger the lazy cleanup.
	go s.cleanupLoop()
	return s
}

func (s *sessionAffinityTemporaryAuthStore) rememberLocal(auth *Auth) {
	if s == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	entry := s.auths[auth.ID]
	if entry == nil {
		entry = &sessionAffinityTemporaryAuthEntry{}
		s.auths[auth.ID] = entry
	}
	entry.auth = cloneAsLocalAffinityAuth(auth)
	entry.expiresAt = now.Add(s.ttl)
	entry.deletedAt = time.Time{}
	// The local credential is healthy again; drop every session's failure streak
	// recorded against the in-memory snapshot.
	entry.sessionFailures = nil
}

func (s *sessionAffinityTemporaryAuthStore) markDeleted(auth *Auth) {
	if s == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	entry := s.auths[auth.ID]
	if entry == nil {
		entry = &sessionAffinityTemporaryAuthEntry{}
		s.auths[auth.ID] = entry
	}
	entry.auth = cloneAsLocalAffinityAuth(auth)
	entry.expiresAt = now.Add(s.ttl)
	if entry.deletedAt.IsZero() {
		entry.deletedAt = now
	}
	// Intentionally preserve sessionFailures: an auth that flaps
	// removed->readded->removed should not lose in-progress per-session state.
	log.Infof("session-affinity temporary auth: local auth removed, retained in memory | auth=%s deleted_at=%s", temporaryAuthLogName(auth), entry.deletedAt.Format(time.RFC3339))
}

func (s *sessionAffinityTemporaryAuthStore) get(authID string) (*Auth, bool) {
	if s == nil || strings.TrimSpace(authID) == "" {
		return nil, false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	entry := s.auths[authID]
	if entry == nil || entry.auth == nil {
		return nil, false
	}
	if now.After(entry.expiresAt) {
		s.deleteLocked(authID, entry, "ttl_expired")
		return nil, false
	}
	entry.hitCount++
	entry.expiresAt = now.Add(s.ttl)
	out := entry.auth.Clone()
	out.temporaryAffinity = true
	out.temporaryAffinityDeletedAt = entry.deletedAt
	return out, true
}

// sessionExceeded reports whether the given session (cache key) has reached the
// per-session consecutive-failure threshold for the temporary auth. The selector
// uses this to stop routing that session to the in-memory credential (when a live
// credential is available) without evicting the shared entry.
func (s *sessionAffinityTemporaryAuthStore) sessionExceeded(authID, cacheKey string) bool {
	if s == nil || strings.TrimSpace(authID) == "" || strings.TrimSpace(cacheKey) == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.auths[authID]
	if entry == nil || entry.sessionFailures == nil {
		return false
	}
	state := entry.sessionFailures[cacheKey]
	return state != nil && state.count >= s.maxFails
}

func (s *sessionAffinityTemporaryAuthStore) recordSuccess(auth *Auth) {
	if s == nil || auth == nil || !auth.temporaryAffinity || strings.TrimSpace(auth.ID) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.auths[auth.ID]
	if entry == nil {
		return
	}
	entry.successCount++
	entry.lastSuccessAt = now
	entry.expiresAt = now.Add(s.ttl)
	// Reset only this session's streak; other sessions are untouched.
	if cacheKey := strings.TrimSpace(auth.temporaryAffinityCacheKey); cacheKey != "" && entry.sessionFailures != nil {
		delete(entry.sessionFailures, cacheKey)
	}
	log.Infof(
		"session-affinity temporary auth: memory auth succeeded after local miss | auth=%s deleted_at=%s session=%s hit_count=%d success_count=%d failure_count=%d",
		temporaryAuthLogName(entry.auth),
		formatTemporaryAuthTime(entry.deletedAt),
		truncateSessionID(strings.TrimSpace(auth.temporaryAffinitySessionID)),
		entry.hitCount,
		entry.successCount,
		entry.failureCount,
	)
}

// recordFailure attributes a failure to the session that produced it. It never
// evicts the shared entry and never mutates the session cache; it only tracks the
// per-session streak. It returns whether that session has now reached the
// per-session threshold. requestID is used to count at most once per client
// request, since the executor may retry several upstream models per request.
func (s *sessionAffinityTemporaryAuthStore) recordFailure(auth *Auth, err error, requestID string) bool {
	if s == nil || auth == nil || !auth.temporaryAffinity || strings.TrimSpace(auth.ID) == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.auths[auth.ID]
	if entry == nil {
		return false
	}
	entry.failureCount++
	entry.lastFailureAt = now
	entry.expiresAt = now.Add(s.ttl)
	cacheKey := strings.TrimSpace(auth.temporaryAffinityCacheKey)
	if cacheKey == "" {
		// Attribution missing: count the aggregate failure but never detach a
		// session. Defensive only; should not happen once the clone chain
		// preserves the cache key end-to-end.
		log.Warnf(
			"session-affinity temporary auth: memory auth failed without session attribution | auth=%s deleted_at=%s failure_count=%d error=%s",
			temporaryAuthLogName(entry.auth),
			formatTemporaryAuthTime(entry.deletedAt),
			entry.failureCount,
			summarizeTemporaryAuthError(err),
		)
		return false
	}
	if entry.sessionFailures == nil {
		entry.sessionFailures = make(map[string]*sessionFailureState)
	}
	state := entry.sessionFailures[cacheKey]
	if state == nil {
		state = &sessionFailureState{}
		entry.sessionFailures[cacheKey] = state
	}
	state.lastAt = now
	if requestID == "" || state.lastRequestID != requestID {
		state.count++
		state.lastRequestID = requestID
	}
	exceeded := state.count >= s.maxFails
	log.Warnf(
		"session-affinity temporary auth: memory auth failed after local miss | auth=%s deleted_at=%s session=%s session_failures=%d failure_count=%d exceeded=%t error=%s",
		temporaryAuthLogName(entry.auth),
		formatTemporaryAuthTime(entry.deletedAt),
		truncateSessionID(strings.TrimSpace(auth.temporaryAffinitySessionID)),
		state.count,
		entry.failureCount,
		exceeded,
		summarizeTemporaryAuthError(err),
	)
	return exceeded
}

func (s *sessionAffinityTemporaryAuthStore) delete(authID string, reason string) {
	if s == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.auths[authID]; entry != nil {
		s.deleteLocked(authID, entry, reason)
	}
}

func (s *sessionAffinityTemporaryAuthStore) deleteLocked(authID string, entry *sessionAffinityTemporaryAuthEntry, reason string) {
	delete(s.auths, authID)
	if entry == nil {
		return
	}
	log.Infof(
		"session-affinity temporary auth: lifecycle ended | auth=%s deleted_at=%s hit_count=%d success_count=%d failure_count=%d reason=%s",
		temporaryAuthLogName(entry.auth),
		formatTemporaryAuthTime(entry.deletedAt),
		entry.hitCount,
		entry.successCount,
		entry.failureCount,
		reason,
	)
}

func (s *sessionAffinityTemporaryAuthStore) cleanupExpiredLocked(now time.Time) {
	// Throttle: this sweep is invoked from hot per-request paths but is pure
	// non-urgent housekeeping (TTL eviction is also enforced lazily by get(), and
	// the cleanupLoop goroutine guarantees reclamation every ttl/2). Skipping it
	// when a sweep ran within the last interval removes the per-request
	// O(num_auths × sessions_per_auth) cost. Callers hold s.mu, so lastCleanup is
	// accessed under the lock. A zero lastCleanup (first call) always sweeps.
	if !s.lastCleanup.IsZero() && now.Sub(s.lastCleanup) < sessionAffinityTemporaryAuthCleanupSweepInterval {
		return
	}
	s.lastCleanup = now
	for authID, entry := range s.auths {
		if entry == nil || now.After(entry.expiresAt) {
			s.deleteLocked(authID, entry, "ttl_expired")
			continue
		}
		s.pruneSessionFailuresLocked(entry, now)
	}
}

// pruneSessionFailuresLocked drops per-session counters for sessions that have
// not failed within a TTL window, bounding the map for long-lived entries that
// are kept alive by a healthy session.
func (s *sessionAffinityTemporaryAuthStore) pruneSessionFailuresLocked(entry *sessionAffinityTemporaryAuthEntry, now time.Time) {
	if entry == nil || len(entry.sessionFailures) == 0 {
		return
	}
	for key, state := range entry.sessionFailures {
		if state == nil || now.Sub(state.lastAt) > s.ttl {
			delete(entry.sessionFailures, key)
		}
	}
}

func (s *sessionAffinityTemporaryAuthStore) cleanupLoop() {
	interval := s.ttl / 2
	if interval <= 0 {
		interval = sessionAffinityTemporaryAuthTTL / 2
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			s.mu.Lock()
			s.cleanupExpiredLocked(now)
			s.mu.Unlock()
		}
	}
}

// Stop terminates the background sweeper goroutine.
func (s *sessionAffinityTemporaryAuthStore) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

func cloneAsLocalAffinityAuth(auth *Auth) *Auth {
	if auth == nil {
		return nil
	}
	out := auth.Clone()
	out.temporaryAffinity = false
	out.temporaryAffinityDeletedAt = time.Time{}
	out.temporaryAffinitySessionID = ""
	out.temporaryAffinityCacheKey = ""
	return out
}

func temporaryAuthLogName(auth *Auth) string {
	if auth == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if label := strings.TrimSpace(auth.Label); label != "" {
		parts = append(parts, "label="+label)
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		parts = append(parts, "id="+id)
	}
	if provider := strings.TrimSpace(auth.Provider); provider != "" {
		parts = append(parts, "provider="+provider)
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " ")
}

func formatTemporaryAuthTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.RFC3339)
}

func summarizeTemporaryAuthError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		return fmt.Sprintf("%s...", msg[:300])
	}
	return msg
}

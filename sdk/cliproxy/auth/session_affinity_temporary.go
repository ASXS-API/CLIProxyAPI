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
)

type sessionAffinityTemporaryAuthEntry struct {
	auth                *Auth
	expiresAt           time.Time
	deletedAt           time.Time
	successCount        int
	failureCount        int
	consecutiveFailures int
	lastSuccessAt       time.Time
	lastFailureAt       time.Time
}

type sessionAffinityTemporaryAuthStore struct {
	mu       sync.Mutex
	auths    map[string]*sessionAffinityTemporaryAuthEntry
	ttl      time.Duration
	maxFails int
}

func newSessionAffinityTemporaryAuthStore(ttl time.Duration, maxFails int) *sessionAffinityTemporaryAuthStore {
	if ttl <= 0 {
		ttl = sessionAffinityTemporaryAuthTTL
	}
	if maxFails <= 0 {
		maxFails = sessionAffinityTemporaryAuthMaxFailures
	}
	return &sessionAffinityTemporaryAuthStore{
		auths:    make(map[string]*sessionAffinityTemporaryAuthEntry),
		ttl:      ttl,
		maxFails: maxFails,
	}
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
	entry.consecutiveFailures = 0
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
	entry.expiresAt = now.Add(s.ttl)
	out := entry.auth.Clone()
	out.temporaryAffinity = true
	out.temporaryAffinityDeletedAt = entry.deletedAt
	return out, true
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
	entry.consecutiveFailures = 0
	entry.lastSuccessAt = now
	entry.expiresAt = now.Add(s.ttl)
	log.Infof(
		"session-affinity temporary auth: memory auth succeeded after local miss | auth=%s deleted_at=%s success_count=%d failure_count=%d",
		temporaryAuthLogName(entry.auth),
		formatTemporaryAuthTime(entry.deletedAt),
		entry.successCount,
		entry.failureCount,
	)
}

func (s *sessionAffinityTemporaryAuthStore) recordFailure(auth *Auth, err error) bool {
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
	entry.consecutiveFailures++
	entry.lastFailureAt = now
	entry.expiresAt = now.Add(s.ttl)
	log.Warnf(
		"session-affinity temporary auth: memory auth failed after local miss | auth=%s deleted_at=%s consecutive_failures=%d failure_count=%d error=%s",
		temporaryAuthLogName(entry.auth),
		formatTemporaryAuthTime(entry.deletedAt),
		entry.consecutiveFailures,
		entry.failureCount,
		summarizeTemporaryAuthError(err),
	)
	if entry.consecutiveFailures < s.maxFails {
		return false
	}
	s.deleteLocked(auth.ID, entry, "consecutive_failures")
	return true
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
		"session-affinity temporary auth: lifecycle ended | auth=%s deleted_at=%s success_count=%d failure_count=%d reason=%s",
		temporaryAuthLogName(entry.auth),
		formatTemporaryAuthTime(entry.deletedAt),
		entry.successCount,
		entry.failureCount,
		reason,
	)
}

func (s *sessionAffinityTemporaryAuthStore) cleanupExpiredLocked(now time.Time) {
	for authID, entry := range s.auths {
		if entry == nil || now.After(entry.expiresAt) {
			s.deleteLocked(authID, entry, "ttl_expired")
		}
	}
}

func cloneAsLocalAffinityAuth(auth *Auth) *Auth {
	if auth == nil {
		return nil
	}
	out := auth.Clone()
	out.temporaryAffinity = false
	out.temporaryAffinityDeletedAt = time.Time{}
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

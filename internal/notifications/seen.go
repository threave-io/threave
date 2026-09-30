package notifications

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/threave-io/threave/internal/store"
)

type completionKey struct {
	sessionID string
	seq       int64
}

// Seen acknowledges a durable terminal event across all devices. It does not
// require a push subscription, and never acknowledges approval requests.
func (s *Service) Seen(ctx context.Context, sessionID string, seq int64) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 128 || seq <= 0 {
		return store.ErrInvalidArgument
	}
	event, err := s.store.GetEvent(ctx, sessionID, seq)
	if errors.Is(err, store.ErrNotFound) {
		return store.ErrInvalidArgument
	}
	if err != nil {
		return err
	}
	if !isTerminalRunEvent(event.Type) {
		return store.ErrInvalidArgument
	}
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	now := time.Now()
	s.pruneSeen(now)
	if s.seenCompletions == nil {
		s.seenCompletions = make(map[completionKey]time.Time)
	}
	key := completionKey{sessionID, seq}
	if _, exists := s.seenCompletions[key]; !exists && len(s.seenCompletions) >= maxForegroundAcks {
		var oldest completionKey
		var expiry time.Time
		for k, v := range s.seenCompletions {
			if expiry.IsZero() || v.Before(expiry) {
				oldest, expiry = k, v
			}
		}
		delete(s.seenCompletions, oldest)
	}
	s.seenCompletions[key] = now.Add(foregroundAckTTL)
	return nil
}

func (s *Service) completionSeen(sessionID string, seq int64, now time.Time) bool {
	s.ackMu.Lock()
	defer s.ackMu.Unlock()
	s.pruneSeen(now)
	return s.seenCompletions[completionKey{sessionID, seq}].After(now)
}

func (s *Service) pruneSeen(now time.Time) {
	for key, expiry := range s.seenCompletions {
		if !expiry.After(now) {
			delete(s.seenCompletions, key)
		}
	}
}

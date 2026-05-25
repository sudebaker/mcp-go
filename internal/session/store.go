package session

import (
	"sync"
	"time"
)

type entry struct {
	userID    string
	createdAt time.Time
}

type Store struct {
	mu       sync.RWMutex
	sessions map[string]entry
	maxAge   time.Duration
}

func New() *Store {
	return &Store{
		sessions: make(map[string]entry),
		maxAge:   24 * time.Hour,
	}
}

func (s *Store) Set(sessionID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = entry{userID: userID, createdAt: time.Now()}
}

func (s *Store) Get(sessionID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.sessions[sessionID]
	if !ok || time.Since(e.createdAt) > s.maxAge {
		return "", false
	}
	return e.userID, true
}

func (s *Store) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

func (s *Store) GetAll() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	result := make(map[string]string, len(s.sessions))
	for k, e := range s.sessions {
		if now.Sub(e.createdAt) <= s.maxAge {
			result[k] = e.userID
		}
	}
	return result
}

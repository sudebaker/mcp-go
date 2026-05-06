package session

import (
	"sync"
)

type Store struct {
	mu       sync.RWMutex
	sessions map[string]string
}

func New() *Store {
	return &Store{
		sessions: make(map[string]string),
	}
}

func (s *Store) Set(sessionID, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = userID
}

func (s *Store) Get(sessionID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	userID, ok := s.sessions[sessionID]
	return userID, ok
}

func (s *Store) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

func (s *Store) GetAll() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.sessions))
	for k, v := range s.sessions {
		result[k] = v
	}
	return result
}
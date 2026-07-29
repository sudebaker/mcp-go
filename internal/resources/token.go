package resources

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path"
	"sync"
	"time"
)

type TokenStore struct {
	mu     sync.RWMutex
	tokens map[string]*tokenEntry
}

type tokenEntry struct {
	Bucket    string
	Key       string
	Name      string
	UserID    string
	SessionID string
	expiresAt time.Time
	used      bool
}

func NewTokenStore() *TokenStore {
	return &TokenStore{tokens: map[string]*tokenEntry{}}
}

func (t *TokenStore) Issue(bucket, key, userID, sessionID string, ttl time.Duration) string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	t.mu.Lock()
	t.tokens[token] = &tokenEntry{
		Bucket:    bucket,
		Key:       key,
		Name:      path.Base(key),
		UserID:    userID,
		SessionID: sessionID,
		expiresAt: time.Now().Add(ttl),
		used:      false,
	}
	t.mu.Unlock()
	return token
}

func (t *TokenStore) Validate(token string) (*tokenEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.tokens[token]
	if !ok {
		return nil, errors.New("invalid token")
	}
	if time.Now().After(e.expiresAt) {
		delete(t.tokens, token)
		return nil, errors.New("token expired")
	}
	if e.used {
		delete(t.tokens, token)
		return nil, errors.New("token already used")
	}
	e.used = true
	return e, nil
}

func (t *TokenStore) Cleanup() {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for k, e := range t.tokens {
		if now.After(e.expiresAt) {
			delete(t.tokens, k)
		}
	}
}

func StartCleanup(store *TokenStore, interval time.Duration) *time.Ticker {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			store.Cleanup()
		}
	}()
	return ticker
}

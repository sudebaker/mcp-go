package resources

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/sudebaker/mcp-go/internal/session"
)

const (
	defaultBucket = "users"
	tokenTTL      = 60 * time.Second
	maxLocalFile  = 50 * 1024 * 1024
)

var (
	ErrUnauthenticated   = errors.New("session not authenticated")
	ErrUnauthorized      = errors.New("unauthorized resource")
	ErrInvalidURI        = errors.New("invalid resource URI")
	ErrUnsupportedArg    = errors.New("unsupported resource argument")
	ErrPathNotAllowed    = errors.New("local path outside allowed directories")
	ErrLocalFileTooLarge = errors.New("local file exceeds size limit")
)

var (
	allowedLocalRootsDefault = []string{"/data/input", "/data/uploads"}
	allowedLocalRoots        []string
)

func init() {
	setAllowedLocalRoots(allowedLocalRootsDefault)
}

func setAllowedLocalRoots(roots []string) {
	allowedLocalRoots = make([]string, len(roots))
	for i, r := range roots {
		allowedLocalRoots[i] = filepath.Clean(r)
	}
}

type ResourceManager struct {
	storage  Storage
	sessions *session.Store
	tokens   *TokenStore
}

func NewResourceManager(storage Storage, sessions *session.Store) *ResourceManager {
	return &ResourceManager{
		storage:  storage,
		sessions: sessions,
		tokens:   NewTokenStore(),
	}
}

func (m *ResourceManager) Tokens() *TokenStore {
	return m.tokens
}

func (m *ResourceManager) Storage() Storage {
	return m.storage
}

func (m *ResourceManager) userBucket(userID string) string {
	return defaultBucket
}

func (m *ResourceManager) userPrefix(userID string) string {
	return userID + "/"
}

func (m *ResourceManager) resolveUser(sessionID string) (string, error) {
	userID, ok := m.sessions.Get(sessionID)
	if !ok || userID == "" {
		return "", ErrUnauthenticated
	}
	return userID, nil
}

func (m *ResourceManager) ResolveForTool(ctx context.Context, sessionID, rawArg string) (Resource, error) {
	userID, err := m.resolveUser(sessionID)
	if err != nil {
		return Resource{}, fmt.Errorf("resolve user: %w", err)
	}

	if strings.HasPrefix(rawArg, "res://") {
		token := strings.TrimPrefix(rawArg, "res://")
		return m.resourceFromToken(ctx, token)
	}

	if strings.HasPrefix(rawArg, "file://") || isAbsoluteLocalPath(rawArg) {
		return m.resolveLocalPath(ctx, userID, sessionID, rawArg)
	}

	if rawArg == "__files__" {
		return Resource{}, errors.New("__files__ must be resolved via ResolveManyForTool")
	}

	return Resource{}, fmt.Errorf("%w: %s", ErrUnsupportedArg, rawArg)
}

func (m *ResourceManager) ResolveManyForTool(ctx context.Context, sessionID string, rawArgs []string) ([]Resource, error) {
	out := make([]Resource, 0, len(rawArgs))
	for _, raw := range rawArgs {
		if raw == "__files__" {
			files, err := m.ListForUser(ctx, sessionID, "")
			if err != nil {
				return nil, fmt.Errorf("expand __files__: %w", err)
			}
			for _, f := range files {
				out = append(out, f)
			}
			continue
		}
		r, err := m.ResolveForTool(ctx, sessionID, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *ResourceManager) ListForUser(ctx context.Context, sessionID, prefix string) ([]Resource, error) {
	userID, err := m.resolveUser(sessionID)
	if err != nil {
		return nil, fmt.Errorf("resolve user: %w", err)
	}
	fullPrefix := m.userPrefix(userID) + prefix
	infos, err := m.storage.List(ctx, defaultBucket, fullPrefix)
	if err != nil {
		return nil, fmt.Errorf("list storage: %w", err)
	}
	out := make([]Resource, 0, len(infos))
	for _, info := range infos {
		token := m.tokens.Issue(defaultBucket, info.Key, userID, sessionID, tokenTTL)
		out = append(out, m.newResource(token, info, userID, sessionID))
	}
	return out, nil
}

func (m *ResourceManager) ReadForUser(ctx context.Context, sessionID, uri string) (Resource, error) {
	if !strings.HasPrefix(uri, "res://") {
		return Resource{}, ErrInvalidURI
	}
	token := strings.TrimPrefix(uri, "res://")
	entry, err := m.tokens.Validate(token)
	if err != nil {
		return Resource{}, fmt.Errorf("validate token: %w", err)
	}
	userID, err := m.resolveUser(sessionID)
	if err != nil {
		return Resource{}, fmt.Errorf("resolve user: %w", err)
	}
	if entry.UserID != userID {
		return Resource{}, ErrUnauthorized
	}
	return m.openResource(ctx, entry.Bucket, entry.Key, entry.UserID, sessionID)
}

func (m *ResourceManager) PutForUser(ctx context.Context, sessionID string, key string, r io.Reader, size int64, ct string) (Resource, error) {
	userID, err := m.resolveUser(sessionID)
	if err != nil {
		return Resource{}, fmt.Errorf("resolve user: %w", err)
	}
	fullKey := m.userPrefix(userID) + key
	info, err := m.storage.Put(ctx, defaultBucket, fullKey, r, size, ct)
	if err != nil {
		return Resource{}, fmt.Errorf("store upload: %w", err)
	}
	token := m.tokens.Issue(defaultBucket, info.Key, userID, sessionID, tokenTTL)
	return m.newResource(token, info, userID, sessionID), nil
}

func (m *ResourceManager) newResource(token string, info ObjectInfo, userID, sessionID string) Resource {
	return Resource{
		URI:      "res://" + token,
		Name:     path.Base(info.Key),
		MIMEType: info.ContentType,
		Size:     info.Size,
	}
}

func (m *ResourceManager) resourceFromToken(ctx context.Context, token string) (Resource, error) {
	entry, err := m.tokens.Validate(token)
	if err != nil {
		return Resource{}, fmt.Errorf("validate token: %w", err)
	}
	return m.openResource(ctx, entry.Bucket, entry.Key, entry.UserID, entry.SessionID)
}

func (m *ResourceManager) openResource(ctx context.Context, bucket, key, userID, sessionID string) (Resource, error) {
	reader, err := m.storage.Open(ctx, bucket, key)
	if err != nil {
		return Resource{}, fmt.Errorf("open resource: %w", err)
	}
	info, err := m.storage.Stat(ctx, bucket, key)
	if err != nil {
		reader.Close()
		return Resource{}, fmt.Errorf("stat resource: %w", err)
	}
	res := m.newResource(m.tokens.Issue(bucket, key, userID, sessionID, tokenTTL), info, userID, sessionID)
	res.Reader = reader
	return res, nil
}

func (m *ResourceManager) resolveLocalPath(ctx context.Context, userID, sessionID, raw string) (Resource, error) {
	p := strings.TrimPrefix(raw, "file://")
	p = filepath.Clean(p)

	if !isAllowedLocalPath(p) {
		return Resource{}, fmt.Errorf("%w: %s", ErrPathNotAllowed, raw)
	}

	f, err := os.Open(p)
	if err != nil {
		return Resource{}, fmt.Errorf("open local file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return Resource{}, fmt.Errorf("stat local file: %w", err)
	}
	if stat.IsDir() {
		return Resource{}, fmt.Errorf("local path is a directory: %s", raw)
	}
	if stat.Size() > maxLocalFile {
		return Resource{}, fmt.Errorf("%w: %s", ErrLocalFileTooLarge, raw)
	}

	name := path.Base(p)
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	suffix, err := randomSuffix(8)
	if err != nil {
		return Resource{}, fmt.Errorf("generate local key suffix: %w", err)
	}
	key := fmt.Sprintf("%slocal/%s-%s%s", m.userPrefix(userID), sanitizeKey(base), suffix, ext)

	info, err := m.storage.Put(ctx, defaultBucket, key, f, stat.Size(), "")
	if err != nil {
		return Resource{}, fmt.Errorf("store local file: %w", err)
	}
	token := m.tokens.Issue(defaultBucket, info.Key, userID, sessionID, tokenTTL)
	return m.newResource(token, info, userID, sessionID), nil
}

func isAbsoluteLocalPath(raw string) bool {
	return filepath.IsAbs(raw)
}

func isAllowedLocalPath(p string) bool {
	for _, root := range allowedLocalRoots {
		if p == root {
			return true
		}
		prefix := root + string(filepath.Separator)
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func sanitizeKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteRune('_')
	}
	return b.String()
}

func randomSuffix(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

func resetAllowedLocalRootsForTest(roots []string) {
	setAllowedLocalRoots(roots)
}

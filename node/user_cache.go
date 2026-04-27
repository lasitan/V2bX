package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
)

const userCacheVersion = 1

type userCachePayload struct {
	Version int              `json:"version"`
	Users   []panel.UserInfo `json:"users"`
}

type userCacheStore struct {
	path string
	mu   sync.Mutex
}

func newUserCacheStore(apiHost, nodeType string, nodeID int) *userCacheStore {
	name := sanitizeCacheKey(fmt.Sprintf("%s_%s_%d", apiHost, nodeType, nodeID))
	path := filepath.Join("/etc/V2bX", "cache", "users_"+name+".db")
	return &userCacheStore{path: path}
}

func sanitizeCacheKey(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return "default"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.' || r == '-' || r == '_':
			return r
		default:
			return '_'
		}
	}, v)
}

func (s *userCacheStore) ensure() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(s.path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	initial := userCachePayload{
		Version: userCacheVersion,
		Users:   make([]panel.UserInfo, 0),
	}
	return s.save(initial)
}

func (s *userCacheStore) Load() ([]panel.UserInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensure(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	payload := userCachePayload{}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.Users == nil {
		payload.Users = make([]panel.UserInfo, 0)
	}
	return payload.Users, nil
}

func (s *userCacheStore) SaveAll(users []panel.UserInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensure(); err != nil {
		return err
	}
	payload := userCachePayload{
		Version: userCacheVersion,
		Users:   users,
	}
	return s.save(payload)
}

func (s *userCacheStore) save(payload userCachePayload) error {
	tmp := s.path + ".tmp"
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err = os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

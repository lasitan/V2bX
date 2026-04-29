package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type runtimeTrafficPayload struct {
	StartedAt int64 `json:"started_at"`
	UpdatedAt int64 `json:"updated_at"`
	Upload    int64 `json:"upload"`
	Download  int64 `json:"download"`
	ReportedUpload   int64 `json:"reported_upload"`
	ReportedDownload int64 `json:"reported_download"`
}

type runtimeTrafficStore struct {
	path string
	mu   sync.Mutex
}

func newRuntimeTrafficStore(apiHost, nodeType string, nodeID int) *runtimeTrafficStore {
	name := sanitizeCacheKey(apiHost + "_" + nodeType + "_" + strconv.Itoa(nodeID))
	path := filepath.Join("/etc/V2bX", "cache", "runtime_traffic_"+name+".json")
	return &runtimeTrafficStore{path: path}
}

func (s *runtimeTrafficStore) ensureDir() error {
	return os.MkdirAll(filepath.Dir(s.path), 0o755)
}

func (s *runtimeTrafficStore) ResetSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	now := time.Now().Unix()
	return s.save(runtimeTrafficPayload{
		StartedAt: now,
		UpdatedAt: now,
		Upload:    0,
		Download:  0,
		ReportedUpload:   0,
		ReportedDownload: 0,
	})
}

func (s *runtimeTrafficStore) Add(upload, download int64) error {
	if upload == 0 && download == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	payload, err := s.loadLocked()
	if err != nil {
		return err
	}
	if payload.StartedAt == 0 {
		payload.StartedAt = time.Now().Unix()
	}
	payload.Upload += upload
	payload.Download += download
	payload.UpdatedAt = time.Now().Unix()
	return s.save(payload)
}

func (s *runtimeTrafficStore) AddReported(upload, download int64) error {
	if upload == 0 && download == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	payload, err := s.loadLocked()
	if err != nil {
		return err
	}
	if payload.StartedAt == 0 {
		payload.StartedAt = time.Now().Unix()
	}
	payload.ReportedUpload += upload
	payload.ReportedDownload += download
	payload.UpdatedAt = time.Now().Unix()
	return s.save(payload)
}

func (s *runtimeTrafficStore) loadLocked() (runtimeTrafficPayload, error) {
	payload := runtimeTrafficPayload{}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return payload, nil
		}
		return payload, err
	}
	if len(raw) == 0 {
		return payload, nil
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return runtimeTrafficPayload{}, err
	}
	return payload, nil
}

func (s *runtimeTrafficStore) save(payload runtimeTrafficPayload) error {
	tmp := s.path + ".tmp"
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err = os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

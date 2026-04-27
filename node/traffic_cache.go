package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
)

const trafficCacheVersion = 1

type trafficCacheItem struct {
	UID      int    `json:"uid"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
	Status   string `json:"status"`
}

type trafficCachePayload struct {
	Version   int                `json:"version"`
	UpdatedAt int64              `json:"updated_at"`
	Items     []trafficCacheItem `json:"items"`
}

type trafficCacheStore struct {
	path string
	mu   sync.Mutex
}

func mergeTrafficCacheItems(items []trafficCacheItem) []trafficCacheItem {
	acc := make(map[int]trafficCacheItem, len(items))
	for _, it := range items {
		if it.UID <= 0 {
			continue
		}
		cur := acc[it.UID]
		cur.UID = it.UID
		cur.Upload += it.Upload
		cur.Download += it.Download
		if cur.Status == "" {
			cur.Status = "pending"
		}
		acc[it.UID] = cur
	}
	out := make([]trafficCacheItem, 0, len(acc))
	for _, it := range acc {
		if it.Upload == 0 && it.Download == 0 {
			continue
		}
		out = append(out, it)
	}
	return out
}

func newTrafficCacheStore(apiHost, nodeType string, nodeID int) *trafficCacheStore {
	name := sanitizeCacheKey(apiHost + "_" + nodeType + "_" + strconv.Itoa(nodeID))
	path := filepath.Join("/etc/V2bX", "cache", "traffic_"+name+".db")
	return &trafficCacheStore{path: path}
}

func (s *trafficCacheStore) ensure() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(s.path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return s.savePayload(trafficCachePayload{
		Version:   trafficCacheVersion,
		UpdatedAt: time.Now().Unix(),
		Items:     make([]trafficCacheItem, 0),
	})
}

func (s *trafficCacheStore) LoadPending() ([]panel.UserTraffic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensure(); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	payload := trafficCachePayload{}
	if err = json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	payload.Items = mergeTrafficCacheItems(payload.Items)
	out := make([]panel.UserTraffic, 0, len(payload.Items))
	for _, it := range payload.Items {
		if it.UID <= 0 || (it.Upload == 0 && it.Download == 0) {
			continue
		}
		out = append(out, panel.UserTraffic{
			UID:      it.UID,
			Upload:   it.Upload,
			Download: it.Download,
		})
	}
	return out, nil
}

func (s *trafficCacheStore) SavePending(items []panel.UserTraffic) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensure(); err != nil {
		return err
	}
	payload := trafficCachePayload{
		Version:   trafficCacheVersion,
		UpdatedAt: time.Now().Unix(),
		Items:     make([]trafficCacheItem, 0, len(items)),
	}
	for _, it := range items {
		if it.UID <= 0 || (it.Upload == 0 && it.Download == 0) {
			continue
		}
		payload.Items = append(payload.Items, trafficCacheItem{
			UID:      it.UID,
			Upload:   it.Upload,
			Download: it.Download,
			Status:   "pending",
		})
	}
	payload.Items = mergeTrafficCacheItems(payload.Items)
	return s.savePayload(payload)
}

func (s *trafficCacheStore) ClearReported() error {
	return s.SavePending(nil)
}

func (s *trafficCacheStore) savePayload(payload trafficCachePayload) error {
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

func mergeUserTraffic(a, b []panel.UserTraffic) []panel.UserTraffic {
	acc := make(map[int]panel.UserTraffic, len(a)+len(b))
	for _, it := range a {
		cur := acc[it.UID]
		cur.UID = it.UID
		cur.Upload += it.Upload
		cur.Download += it.Download
		acc[it.UID] = cur
	}
	for _, it := range b {
		cur := acc[it.UID]
		cur.UID = it.UID
		cur.Upload += it.Upload
		cur.Download += it.Download
		acc[it.UID] = cur
	}
	out := make([]panel.UserTraffic, 0, len(acc))
	for _, it := range acc {
		if it.UID <= 0 || (it.Upload == 0 && it.Download == 0) {
			continue
		}
		out = append(out, it)
	}
	return out
}

package hy2

import (
	"net"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/apernet/hysteria/core/v2/server"
	log "github.com/sirupsen/logrus"
)

var _ server.Authenticator = &V2bX{}

type V2bX struct {
	usersMap     map[string]int
	usersHistory map[string]int
	mutex        sync.RWMutex
}

func (v *V2bX) Authenticate(addr net.Addr, auth string, tx uint64) (ok bool, id string) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()
	if _, exists := v.usersMap[auth]; exists {
		return true, auth
	}
	if _, exists := v.usersHistory[auth]; exists {
		return true, auth
	}
	return false, ""
}

func (h *Hysteria2) AddUsers(p *vCore.AddUsersParams) (added int, err error) {
	var wg sync.WaitGroup
	for _, user := range p.Users {
		wg.Add(1)
		go func(u panel.UserInfo) {
			defer wg.Done()
			h.Auth.mutex.Lock()
			h.Auth.usersMap[u.Uuid] = u.Id
			h.Auth.usersHistory[u.Uuid] = u.Id
			h.Auth.mutex.Unlock()
		}(user)
	}
	wg.Wait()
	return len(p.Users), nil
}

func (h *Hysteria2) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	var wg sync.WaitGroup
	for _, user := range users {
		wg.Add(1)
		// Keep counter data for in-flight sessions; map fallback will attribute later.
		go func(u panel.UserInfo) {
			defer wg.Done()
			h.Auth.mutex.Lock()
			delete(h.Auth.usersMap, u.Uuid)
			h.Auth.mutex.Unlock()
		}(user)
	}
	wg.Wait()
	return nil
}

func (h *Hysteria2) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	trafficSlice := make([]panel.UserTraffic, 0)
	var recoveredByHistory int64
	var unresolvedUp int64
	var unresolvedDown int64
	h.Auth.mutex.RLock()
	defer h.Auth.mutex.RUnlock()
	if _, ok := h.Hy2nodes[tag]; !ok {
		return nil, nil
	}
	hook := h.Hy2nodes[tag].TrafficLogger.(*HookServer)
	if v, ok := hook.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			uuid := key.(string)
			traffic := value.(*counter.TrafficStorage)
			var up int64
			var down int64
			if reset {
				up = traffic.UpCounter.Swap(0)
				down = traffic.DownCounter.Swap(0)
			} else {
				up = traffic.UpCounter.Load()
				down = traffic.DownCounter.Load()
			}
			if up != 0 || down != 0 {
				uid := h.Auth.usersMap[uuid]
				recoveredBy := false
				if uid == 0 {
					uid = h.Auth.usersHistory[uuid]
					if uid != 0 {
						recoveredBy = true
					}
				}
				if uid == 0 {
					unresolvedUp += up
					unresolvedDown += down
					if reset && (up != 0 || down != 0) {
						traffic.UpCounter.Add(up)
						traffic.DownCounter.Add(down)
					}
					return true
				}
				if recoveredBy {
					recoveredByHistory += up + down
				}
				trafficSlice = append(trafficSlice, panel.UserTraffic{
					UID:      uid,
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
		if len(trafficSlice) == 0 {
			if reset && (recoveredByHistory > 0 || unresolvedUp > 0 || unresolvedDown > 0) {
				log.WithFields(log.Fields{
					"tag":                     tag,
					"recovered_by_history_mb": float64(recoveredByHistory) / (1024 * 1024),
					"unresolved_up_mb":        float64(unresolvedUp) / (1024 * 1024),
					"unresolved_down_mb":      float64(unresolvedDown) / (1024 * 1024),
				}).Warn("Hysteria2 traffic ownership diagnostics")
			}
			return nil, nil
		}
		if reset && (recoveredByHistory > 0 || unresolvedUp > 0 || unresolvedDown > 0) {
			log.WithFields(log.Fields{
				"tag":                     tag,
				"recovered_by_history_mb": float64(recoveredByHistory) / (1024 * 1024),
				"unresolved_up_mb":        float64(unresolvedUp) / (1024 * 1024),
				"unresolved_down_mb":      float64(unresolvedDown) / (1024 * 1024),
			}).Warn("Hysteria2 traffic ownership diagnostics")
		}
		return trafficSlice, nil
	}
	return nil, nil
}

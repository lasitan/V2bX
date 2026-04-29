package xray

import (
	"context"
	"fmt"
	"strings"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/core/xray/app/dispatcher"
	log "github.com/sirupsen/logrus"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy"
)

func (c *Xray) GetUserManager(tag string) (proxy.UserManager, error) {
	handler, err := c.ihm.GetHandler(context.Background(), tag)
	if err != nil {
		return nil, fmt.Errorf("no such inbound tag: %s", err)
	}
	inboundInstance, ok := handler.(proxy.GetInbound)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.GetInbound", tag)
	}
	userManager, ok := inboundInstance.GetInbound().(proxy.UserManager)
	if !ok {
		return nil, fmt.Errorf("handler %s is not implement proxy.UserManager", tag)
	}
	return userManager, nil
}

func (c *Xray) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	userManager, err := c.GetUserManager(tag)
	if err != nil {
		return fmt.Errorf("get user manager error: %s", err)
	}
	var user string
	c.users.mapLock.Lock()
	defer c.users.mapLock.Unlock()
	for i := range users {
		user = format.UserTag(tag, users[i].Uuid)
		err = userManager.RemoveUser(context.Background(), user)
		if err != nil {
			return err
		}
		// Keep counter data for in-flight sessions; resolve by uidHistory/uidByUUID later.
		delete(c.users.uidMap, user)
		if v, ok := c.dispatcher.LinkManagers.Load(user); ok {
			lm := v.(*dispatcher.LinkManager)
			lm.CloseAll()
			c.dispatcher.LinkManagers.Delete(user)
		}
	}
	return nil
}

func (x *Xray) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	trafficSlice := make([]panel.UserTraffic, 0)
	var recoveredByHistory int64
	var recoveredByUUID int64
	var unresolvedUp int64
	var unresolvedDown int64
	x.users.mapLock.RLock()
	defer x.users.mapLock.RUnlock()
	if v, ok := x.dispatcher.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			email := key.(string)
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
				uid := x.users.uidMap[email]
				recoveredBy := ""
				if uid == 0 {
					uid = x.users.uidHistory[email]
					if uid != 0 {
						recoveredBy = "history"
					}
				}
				if uid == 0 {
					if idx := strings.LastIndex(email, "|"); idx >= 0 && idx+1 < len(email) {
						uuid := email[idx+1:]
						uid = x.users.uidByUUID[uuid]
						if uid != 0 {
							recoveredBy = "uuid"
						}
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
				if recoveredBy == "history" {
					recoveredByHistory += up + down
				} else if recoveredBy == "uuid" {
					recoveredByUUID += up + down
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
			if reset && (recoveredByHistory > 0 || recoveredByUUID > 0 || unresolvedUp > 0 || unresolvedDown > 0) {
				log.WithFields(log.Fields{
					"tag":                     tag,
					"recovered_by_history_mb": float64(recoveredByHistory) / (1024 * 1024),
					"recovered_by_uuid_mb":    float64(recoveredByUUID) / (1024 * 1024),
					"unresolved_up_mb":        float64(unresolvedUp) / (1024 * 1024),
					"unresolved_down_mb":      float64(unresolvedDown) / (1024 * 1024),
				}).Warn("Xray traffic ownership diagnostics")
			}
			return nil, nil
		}
		if reset && (recoveredByHistory > 0 || recoveredByUUID > 0 || unresolvedUp > 0 || unresolvedDown > 0) {
			log.WithFields(log.Fields{
				"tag":                     tag,
				"recovered_by_history_mb": float64(recoveredByHistory) / (1024 * 1024),
				"recovered_by_uuid_mb":    float64(recoveredByUUID) / (1024 * 1024),
				"unresolved_up_mb":        float64(unresolvedUp) / (1024 * 1024),
				"unresolved_down_mb":      float64(unresolvedDown) / (1024 * 1024),
			}).Warn("Xray traffic ownership diagnostics")
		}
		return trafficSlice, nil
	}
	return nil, nil
}

func (c *Xray) AddUsers(p *vCore.AddUsersParams) (added int, err error) {
	c.users.mapLock.Lock()
	defer c.users.mapLock.Unlock()
	for i := range p.Users {
		userTag := format.UserTag(p.Tag, p.Users[i].Uuid)
		c.users.uidMap[userTag] = p.Users[i].Id
		c.users.uidHistory[userTag] = p.Users[i].Id
		c.users.uidByUUID[p.Users[i].Uuid] = p.Users[i].Id
	}
	var users []*protocol.User
	switch p.NodeInfo.Type {
	case "vmess":
		users = buildVmessUsers(p.Tag, p.Users)
	case "vless":
		users = buildVlessUsers(p.Tag, p.Users, p.VAllss.Flow)
	case "trojan":
		users = buildTrojanUsers(p.Tag, p.Users)
	case "shadowsocks":
		users = buildSSUsers(p.Tag,
			p.Users,
			p.Shadowsocks.Cipher,
			p.Shadowsocks.ServerKey)
	default:
		return 0, fmt.Errorf("unsupported node type: %s", p.NodeInfo.Type)
	}
	man, err := c.GetUserManager(p.Tag)
	if err != nil {
		return 0, fmt.Errorf("get user manager error: %s", err)
	}
	for _, u := range users {
		mUser, err := u.ToMemoryUser()
		if err != nil {
			return 0, err
		}
		err = man.AddUser(context.Background(), mUser)
		if err != nil {
			return 0, err
		}
	}
	return len(users), nil
}

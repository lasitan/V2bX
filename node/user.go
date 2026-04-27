package node

import (
	"strconv"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) reportOnlineUsersNow() (err error) {
	onlineDevice, err := c.limiter.GetCurrentOnlineDevice()
	if err != nil {
		log.Print(err)
		return nil
	}
	if onlineDevice == nil || len(*onlineDevice) == 0 {
		return nil
	}
	// Only report user has traffic > 100kb to allow ping test
	userTraffic, _ := c.server.GetUserTrafficSlice(c.tag, false)
	var result []panel.OnlineUser
	nocountUID := make(map[int]struct{})
	for _, traffic := range userTraffic {
		total := traffic.Upload + traffic.Download
		if total < int64(c.Options.DeviceOnlineMinTraffic*1000) {
			nocountUID[traffic.UID] = struct{}{}
		}
	}
	for _, online := range *onlineDevice {
		if _, ok := nocountUID[online.UID]; !ok {
			result = append(result, online)
		}
	}
	data := make(map[int][]string)
	for _, onlineuser := range result {
		data[onlineuser.UID] = append(data[onlineuser.UID], onlineuser.IP)
	}
	if err = c.apiClient.ReportNodeOnlineUsers(&data); err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Info("Report online users failed")
	}
	_ = time.Now() // keep import stable if log build tags vary
	return nil
}

func (c *Controller) reportUserTrafficTask() (err error) {
	currentTraffic, _ := c.server.GetUserTrafficSlice(c.tag, true)
	pendingTraffic := make([]panel.UserTraffic, 0)
	if c.trafficCache != nil {
		if cached, cacheErr := c.trafficCache.LoadPending(); cacheErr != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": cacheErr,
			}).Warn("Load pending traffic cache failed")
		} else {
			pendingTraffic = cached
		}
	}
	mergedTraffic := mergeUserTraffic(pendingTraffic, currentTraffic)
	if len(mergedTraffic) > 0 {
		err = c.apiClient.ReportUserTraffic(mergedTraffic)
		if err != nil {
			if c.trafficCache != nil {
				_ = c.trafficCache.SavePending(mergedTraffic)
			}
			log.WithFields(log.Fields{
				"tag":      c.tag,
				"err":      err,
				"cache":    "pending",
				"cache_db": "traffic",
			}).Info("Report user traffic failed, saved to local DB")
		} else if c.trafficCache != nil {
			_ = c.trafficCache.ClearReported()
		}
	}

	_ = c.reportOnlineUsersNow()

	currentTraffic = nil
	mergedTraffic = nil
	return nil
}

func compareUserList(old, new []panel.UserInfo) (deleted, added []panel.UserInfo) {
	oldMap := make(map[string]int)
	for i, user := range old {
		key := user.Uuid + strconv.Itoa(user.SpeedLimit)
		oldMap[key] = i
	}

	for _, user := range new {
		key := user.Uuid + strconv.Itoa(user.SpeedLimit)
		if _, exists := oldMap[key]; !exists {
			added = append(added, user)
		} else {
			delete(oldMap, key)
		}
	}

	for _, index := range oldMap {
		deleted = append(deleted, old[index])
	}

	return deleted, added
}

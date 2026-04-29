package node

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	log "github.com/sirupsen/logrus"
)

const bytesPerMB = 1024 * 1024

func bytesToMB(v int64) float64 {
	return float64(v) / bytesPerMB
}

func buildTopUserTrafficFields(items []panel.UserTraffic) (string, string, string) {
	if len(items) == 0 {
		return "", "", ""
	}
	sorted := make([]panel.UserTraffic, 0, len(items))
	for _, it := range items {
		if it.UID <= 0 || (it.Upload == 0 && it.Download == 0) {
			continue
		}
		sorted = append(sorted, it)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Upload+sorted[i].Download > sorted[j].Upload+sorted[j].Download
	})
	formatOne := func(it panel.UserTraffic) string {
		return fmt.Sprintf(
			"uid=%d up=%.6fMB down=%.6fMB total=%.6fMB",
			it.UID,
			bytesToMB(it.Upload),
			bytesToMB(it.Download),
			bytesToMB(it.Upload+it.Download),
		)
	}
	var top1, top2, top3 string
	if len(sorted) > 0 {
		top1 = formatOne(sorted[0])
	}
	if len(sorted) > 1 {
		top2 = formatOne(sorted[1])
	}
	if len(sorted) > 2 {
		top3 = formatOne(sorted[2])
	}
	return top1, top2, top3
}

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
	var runtimeUpload int64
	var runtimeDownload int64
	for _, t := range currentTraffic {
		runtimeUpload += t.Upload
		runtimeDownload += t.Download
	}
	if c.runtimeTraffic != nil {
		if rtErr := c.runtimeTraffic.Add(runtimeUpload, runtimeDownload); rtErr != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": rtErr}).Warn("Runtime traffic cache add failed")
		}
	}
	pendingTraffic := make([]panel.UserTraffic, 0)
	var pendingUpload int64
	var pendingDownload int64
	if c.trafficCache != nil {
		if cached, cacheErr := c.trafficCache.LoadPending(); cacheErr != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": cacheErr,
			}).Warn("Load pending traffic cache failed")
		} else {
			pendingTraffic = cached
			for _, t := range pendingTraffic {
				pendingUpload += t.Upload
				pendingDownload += t.Download
			}
		}
	}
	mergedTraffic := mergeUserTraffic(pendingTraffic, currentTraffic)
	var mergedUpload int64
	var mergedDownload int64
	for _, t := range mergedTraffic {
		mergedUpload += t.Upload
		mergedDownload += t.Download
	}
	topUser1, topUser2, topUser3 := buildTopUserTrafficFields(mergedTraffic)
	log.WithFields(log.Fields{
		"tag":                c.tag,
		"current_up_mb":      bytesToMB(runtimeUpload),
		"current_down_mb":    bytesToMB(runtimeDownload),
		"pending_up_mb":      bytesToMB(pendingUpload),
		"pending_down_mb":    bytesToMB(pendingDownload),
		"report_up_mb":       bytesToMB(mergedUpload),
		"report_down_mb":     bytesToMB(mergedDownload),
		"report_user_count":  len(mergedTraffic),
		"top_user_1":         topUser1,
		"top_user_2":         topUser2,
		"top_user_3":         topUser3,
		"ownership_diag_tip": "see *traffic ownership diagnostics* logs for unresolved/recovered details",
		"traffic_unit":       "MB",
		"traffic_report_run": true,
	}).Info("Traffic report snapshot")
	if len(mergedTraffic) > 0 {
		err = c.apiClient.ReportUserTraffic(mergedTraffic)
		if err != nil {
			c.trafficReportFailCount++
			if c.trafficCache != nil {
				if saveErr := c.trafficCache.SavePending(mergedTraffic); saveErr != nil {
					log.WithFields(log.Fields{"tag": c.tag, "err": saveErr}).Warn("Save pending traffic failed")
				}
			}
			log.WithFields(log.Fields{
				"tag":      c.tag,
				"err":      err,
				"cache":    "pending",
				"cache_db": "traffic",
				"chain":    "traffic_report",
				"fail_seq": c.trafficReportFailCount,
			}).Info("Report user traffic failed, saved to local DB")
			if c.trafficReportFailCount >= chainFailResetThreshold {
				c.apiClient.ResetTrafficReportChain()
				c.trafficReportFailCount = 0
				log.WithField("tag", c.tag).Warn("traffic_report chain reset after repeated failures")
			}
		} else if c.trafficCache != nil {
			c.trafficReportFailCount = 0
			if clearErr := c.trafficCache.ClearReported(); clearErr != nil {
				log.WithFields(log.Fields{"tag": c.tag, "err": clearErr}).Warn("Clear pending traffic failed")
			}
			if c.runtimeTraffic != nil {
				if rtErr := c.runtimeTraffic.AddReported(mergedUpload, mergedDownload); rtErr != nil {
					log.WithFields(log.Fields{"tag": c.tag, "err": rtErr}).Warn("Runtime traffic reported add failed")
				}
			}
			log.WithFields(log.Fields{
				"tag":            c.tag,
				"reported_up_mb": bytesToMB(mergedUpload),
				"reported_dn_mb": bytesToMB(mergedDownload),
				"user_count":     len(mergedTraffic),
				"traffic_unit":   "MB",
			}).Info("Report user traffic success")
		}
	} else {
		log.WithFields(log.Fields{
			"tag":          c.tag,
			"traffic_unit": "MB",
		}).Info("Report user traffic skipped: no traffic to report")
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

package node

import (
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
)

const chainFailResetThreshold = 5

func (c *Controller) startTasks(node *panel.NodeInfo) {
	// fetch node info task
	c.nodeInfoMonitorPeriodic = &task.Task{
		Interval: node.PullInterval,
		Execute:  c.nodeInfoMonitor,
	}
	// fetch user list task
	c.userReportPeriodic = &task.Task{
		Interval: node.PushInterval,
		Execute:  c.reportUserTrafficTask,
	}
	// expire online users task (offline after idle)
	c.onlineIpReportPeriodic = &task.Task{
		Interval: 5 * time.Second,
		Execute:  c.expireOnlineTask,
	}
	log.WithField("tag", c.tag).Info("Start monitor node status")
	// delay to start nodeInfoMonitor
	_ = c.nodeInfoMonitorPeriodic.Start(false)
	log.WithField("tag", c.tag).Info("Start report node status")
	_ = c.userReportPeriodic.Start(false)
	_ = c.onlineIpReportPeriodic.Start(false)
	if node.Security == panel.Tls {
		switch c.CertConfig.CertMode {
		case "none", "", "file", "self":
		default:
			c.renewCertPeriodic = &task.Task{
				Interval: time.Hour * 24,
				Execute:  c.renewCertTask,
			}
			log.WithField("tag", c.tag).Info("Start renew cert")
			// delay to start renewCert
			_ = c.renewCertPeriodic.Start(true)
		}
	}
	if c.LimitConfig.EnableDynamicSpeedLimit {
		c.traffic = make(map[string]int64)
		c.dynamicSpeedLimitPeriodic = &task.Task{
			Interval: time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.Periodic) * time.Second,
			Execute:  c.SpeedChecker,
		}
		log.Printf("[%s: %d] Start dynamic speed limit", c.apiClient.NodeType, c.apiClient.NodeId)
	}
}

func (c *Controller) applyUserSnapshot(newU []panel.UserInfo) error {
	oldUsers := c.userList
	deleted, added := compareUserList(oldUsers, newU)
	if len(added) == 0 && len(deleted) == 0 {
		// No-op: avoid rebuilding user map/counters and losing in-flight traffic stats.
		return nil
	}
	if len(deleted) > 0 {
		c.flushCurrentTrafficToCache("before_user_snapshot_delete")
		if err := c.server.DelUsers(deleted, c.tag, c.info); err != nil {
			return err
		}
	}
	if len(added) > 0 {
		if _, err := c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			NodeInfo: c.info,
			Users:    added,
		}); err != nil {
			return err
		}
	}
	if len(added) > 0 || len(deleted) > 0 {
		c.limiter.UpdateUser(c.tag, added, deleted)
		if c.LimitConfig.EnableDynamicSpeedLimit {
			for i := range deleted {
				delete(c.traffic, deleted[i].Uuid)
			}
		}
	}
	c.userList = newU
	if c.userCache != nil {
		_ = c.userCache.SaveAll(c.userList)
	}
	if len(added) > 0 || len(deleted) > 0 {
		log.WithField("tag", c.tag).Infof("用户快照已全量覆盖，删除 %d 个用户，新增 %d 个用户", len(deleted), len(added))
	}
	return nil
}

func (c *Controller) nodeInfoMonitor() (err error) {
	// Pull chains are independent:
	// a failure on one chain should not block other chains in this round.
	var (
		newN     *panel.NodeInfo
		newU     []panel.UserInfo
		newA     map[int]int
		nodeErr  error
		userErr  error
		aliveErr error
	)
	var pullWG sync.WaitGroup
	pullWG.Add(3)
	go func() {
		defer pullWG.Done()
		newN, nodeErr = c.apiClient.GetNodeInfo()
	}()
	go func() {
		defer pullWG.Done()
		newU, userErr = c.apiClient.GetUserList()
	}()
	go func() {
		defer pullWG.Done()
		newA, aliveErr = c.apiClient.GetUserAlive()
	}()
	pullWG.Wait()

	if nodeErr != nil {
		c.nodeInfoFailCount++
		log.WithFields(log.Fields{
			"tag":      c.tag,
			"err":      nodeErr,
			"chain":    "node_info",
			"fail_seq": c.nodeInfoFailCount,
		}).Error("Get node info failed")
		if c.nodeInfoFailCount >= chainFailResetThreshold {
			c.apiClient.ResetNodeInfoChain()
			c.nodeInfoFailCount = 0
			log.WithField("tag", c.tag).Warn("node_info chain reset after repeated failures")
		}
	} else {
		c.nodeInfoFailCount = 0
	}
	if userErr != nil {
		c.userListFailCount++
		log.WithFields(log.Fields{
			"tag":      c.tag,
			"err":      userErr,
			"chain":    "user_list",
			"fail_seq": c.userListFailCount,
		}).Error("Get user list failed")
		if c.userListFailCount >= chainFailResetThreshold {
			c.apiClient.ResetUserListChain()
			c.userListFailCount = 0
			log.WithField("tag", c.tag).Warn("user_list chain reset after repeated failures")
		}
	} else {
		c.userListFailCount = 0
	}
	if aliveErr != nil {
		c.aliveListFailCount++
		log.WithFields(log.Fields{
			"tag":      c.tag,
			"err":      aliveErr,
			"chain":    "alive_list",
			"fail_seq": c.aliveListFailCount,
		}).Error("Get alive list failed")
		if c.aliveListFailCount >= chainFailResetThreshold {
			c.apiClient.ResetAliveChain()
			c.aliveListFailCount = 0
			log.WithField("tag", c.tag).Warn("alive_list chain reset after repeated failures")
		}
	} else {
		c.aliveListFailCount = 0
	}
	if newN != nil {
		c.flushCurrentTrafficToCache("before_node_reload")
		c.info = newN
		// nodeInfo changed
		if newU != nil {
			c.userList = newU
		}
		c.traffic = make(map[string]int64)
		// Remove old node
		log.WithField("tag", c.tag).Info("Node changed, reload")
		err = c.server.DelNode(c.tag)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Panic("Delete node failed")
			return nil
		}

		// Update limiter
		if len(c.Options.Name) == 0 {
			c.tag = c.buildNodeTag(newN)
			// Remove Old limiter
			limiter.DeleteLimiter(c.tag)
			// Add new Limiter
			l := limiter.AddLimiter(c.tag, &c.LimitConfig, c.userList, newA)
			c.limiter = l
		}
		// update alive list
		if newA != nil {
			c.limiter.AliveList = newA
		}
		// Update rule
		err = c.limiter.UpdateRule(&newN.Rules)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Update Rule failed")
			return nil
		}

		// check cert
		if newN.Security == panel.Tls {
			err = c.requestCert()
			if err != nil {
				log.WithFields(log.Fields{
					"tag": c.tag,
					"err": err,
				}).Error("Request cert failed")
				return nil
			}
		}
		// add new node
		err = c.server.AddNode(c.tag, newN, c.Options)
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Panic("Add node failed")
			return nil
		}
		_, err = c.server.AddUsers(&vCore.AddUsersParams{
			Tag:      c.tag,
			Users:    c.userList,
			NodeInfo: newN,
		})
		if err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Error("Add users failed")
			return nil
		}
		if c.userCache != nil {
			_ = c.userCache.SaveAll(c.userList)
		}
		// Check interval
		if c.nodeInfoMonitorPeriodic.Interval != newN.PullInterval &&
			newN.PullInterval != 0 {
			c.nodeInfoMonitorPeriodic.Interval = newN.PullInterval
			c.nodeInfoMonitorPeriodic.Close()
			_ = c.nodeInfoMonitorPeriodic.Start(false)
		}
		if c.userReportPeriodic.Interval != newN.PushInterval &&
			newN.PushInterval != 0 {
			c.userReportPeriodic.Interval = newN.PushInterval
			c.userReportPeriodic.Close()
			_ = c.userReportPeriodic.Start(false)
		}
		log.WithField("tag", c.tag).Infof("拉取到 %d 个新用户", len(c.userList))
		// exit
		return nil
	}
	// update alive list
	if newA != nil {
		c.limiter.AliveList = newA
	}
	// node no changed, check users
	if userErr != nil || len(newU) == 0 {
		return nil
	}
	if err = c.applyUserSnapshot(newU); err != nil {
		log.WithFields(log.Fields{
			"tag": c.tag,
			"err": err,
		}).Error("全量覆盖用户失败")
	}
	return nil
}

func (c *Controller) SpeedChecker() error {
	for u, t := range c.traffic {
		if t >= c.LimitConfig.DynamicSpeedLimitConfig.Traffic {
			err := c.limiter.UpdateDynamicSpeedLimit(c.tag, u,
				c.LimitConfig.DynamicSpeedLimitConfig.SpeedLimit,
				time.Now().Add(time.Duration(c.LimitConfig.DynamicSpeedLimitConfig.ExpireTime)*time.Minute))
			log.WithField("err", err).Error("Update dynamic speed limit failed")
			delete(c.traffic, u)
		}
	}
	return nil
}

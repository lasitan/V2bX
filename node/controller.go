package node

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
)

type Controller struct {
	server                    vCore.Core
	apiClient                 *panel.Client
	userCache                 *userCacheStore
	trafficCache              *trafficCacheStore
	runtimeTraffic            *runtimeTrafficStore
	tag                       string
	limiter                   *limiter.Limiter
	traffic                   map[string]int64
	userList                  []panel.UserInfo
	aliveMap                  map[int]int
	info                      *panel.NodeInfo
	nodeInfoMonitorPeriodic   *task.Task
	userReportPeriodic        *task.Task
	renewCertPeriodic         *task.Task
	dynamicSpeedLimitPeriodic *task.Task
	onlineIpReportPeriodic    *task.Task
	onlineReportCh            chan struct{}
	onlineReportStopCh        chan struct{}
	nodeInfoFailCount         int
	userListFailCount         int
	aliveListFailCount        int
	trafficReportFailCount    int
	*conf.Options
}

// NewController return a Node controller with default parameters.
func NewController(server vCore.Core, api *panel.Client, config *conf.Options) *Controller {
	controller := &Controller{
		server:    server,
		Options:   config,
		apiClient: api,
	}
	return controller
}

// Start implement the Start() function of the service interface
func (c *Controller) Start() error {
	// First fetch Node Info
	var err error
	defer func() {
		if err == nil {
			return
		}
		// Best-effort cleanup for retryable startup.
		if c.tag != "" {
			limiter.DeleteLimiter(c.tag)
			_ = c.server.DelNode(c.tag)
		}
	}()

	c.userCache = newUserCacheStore(c.apiClient.APIHost, c.apiClient.NodeType, c.apiClient.NodeId)
	c.trafficCache = newTrafficCacheStore(c.apiClient.APIHost, c.apiClient.NodeType, c.apiClient.NodeId)
	c.runtimeTraffic = newRuntimeTrafficStore(c.apiClient.APIHost, c.apiClient.NodeType, c.apiClient.NodeId)
	if c.runtimeTraffic != nil {
		_ = c.runtimeTraffic.ResetSession()
	}
	cachedUsers, cacheErr := c.userCache.Load()
	if cacheErr != nil {
		log.WithFields(log.Fields{
			"api_host": c.apiClient.APIHost,
			"node_id":  c.apiClient.NodeId,
			"err":      cacheErr,
		}).Warn("用户缓存读取失败")
	}

	var (
		node       *panel.NodeInfo
		pulledUsers []panel.UserInfo
		pullErr    error
		aliveErr   error
	)
	var startWG sync.WaitGroup
	startWG.Add(3)
	go func() {
		defer startWG.Done()
		node, err = c.apiClient.GetNodeInfo()
	}()
	go func() {
		defer startWG.Done()
		// fetch full users from panel; startup can continue with cache when panel is temporarily unavailable
		pulledUsers, pullErr = c.apiClient.GetUserList()
	}()
	go func() {
		defer startWG.Done()
		c.aliveMap, aliveErr = c.apiClient.GetUserAlive()
	}()
	startWG.Wait()

	if err != nil {
		return fmt.Errorf("获取节点信息失败: %s", err)
	}
	if pullErr != nil {
		log.WithFields(log.Fields{
			"api_host": c.apiClient.APIHost,
			"node_id":  c.apiClient.NodeId,
			"err":      pullErr,
		}).Warn("拉取用户列表失败，尝试使用本地缓存启动")
	}

	useCacheFirst := len(cachedUsers) > 0
	if useCacheFirst {
		c.userList = cachedUsers
	} else if pullErr == nil && len(pulledUsers) > 0 {
		c.userList = pulledUsers
		if c.userCache != nil {
			_ = c.userCache.SaveAll(c.userList)
		}
	} else if pullErr != nil {
		return fmt.Errorf("获取用户列表失败且缓存为空: %s", pullErr)
	} else {
		return errors.New("添加用户失败: 面板未返回任何用户且缓存为空")
	}

	if aliveErr != nil {
		log.WithFields(log.Fields{
			"api_host": c.apiClient.APIHost,
			"node_id":  c.apiClient.NodeId,
			"err":      aliveErr,
		}).Warn("获取用户在线列表失败，使用空在线列表继续")
		c.aliveMap = make(map[int]int)
	}
	if len(c.Options.Name) == 0 {
		c.tag = c.buildNodeTag(node)
	} else {
		c.tag = c.Options.Name
	}

	// add limiter
	l := limiter.AddLimiter(c.tag, &c.LimitConfig, c.userList, c.aliveMap)
	// add rule limiter
	if err = l.UpdateRule(&node.Rules); err != nil {
		return fmt.Errorf("更新规则失败: %s", err)
	}
	c.limiter = l
	c.onlineReportCh = make(chan struct{}, 1)
	c.onlineReportStopCh = make(chan struct{})
	c.limiter.SetOnlineStateHook(func(evt limiter.OnlineStateEvent) {
		select {
		case c.onlineReportCh <- struct{}{}:
		default:
		}
	})
	go c.onlineReportLoop()
	if node.Security == panel.Tls {
		err = c.requestCert()
		if err != nil {
			return fmt.Errorf("申请证书失败: %s", err)
		}
	}
	// Add new tag
	err = c.server.AddNode(c.tag, node, c.Options)
	if err != nil {
		return fmt.Errorf("添加节点失败: %s", err)
	}
	added, err := c.server.AddUsers(&vCore.AddUsersParams{
		Tag:      c.tag,
		Users:    c.userList,
		NodeInfo: node,
	})
	if err != nil {
		return fmt.Errorf("添加用户失败: %s", err)
	}
	log.WithField("tag", c.tag).Infof("拉取到 %d 个新用户", added)
	c.info = node
	c.startTasks(node)

	// Startup path: run with cache first, then hot switch to freshly pulled full snapshot.
	if useCacheFirst && pullErr == nil && len(pulledUsers) > 0 {
		if err = c.applyUserSnapshot(pulledUsers); err != nil {
			log.WithFields(log.Fields{
				"tag": c.tag,
				"err": err,
			}).Warn("缓存切换到最新用户失败")
		} else {
			log.WithField("tag", c.tag).Info("已从缓存用户切换到主控最新用户")
		}
	}

	return nil
}

func (c *Controller) onlineReportLoop() {
	debounce := 300 * time.Millisecond
	var t *time.Timer
	var tc <-chan time.Time
	for {
		select {
		case <-c.onlineReportStopCh:
			if t != nil {
				t.Stop()
			}
			return
		case <-c.onlineReportCh:
			if t == nil {
				t = time.NewTimer(debounce)
				tc = t.C
			} else {
				if !t.Stop() {
					select {
					case <-t.C:
					default:
					}
				}
				t.Reset(debounce)
			}
		case <-tc:
			tc = nil
			t = nil
			for {
				select {
				case <-c.onlineReportCh:
				default:
					goto REPORT
				}
			}
		REPORT:
			_ = c.reportOnlineUsersNow()
		}
	}
}

func (c *Controller) expireOnlineTask() error {
	// 15s no activity counts as offline
	_ = c.limiter.ExpireOnlineBefore(time.Now().Add(-15 * time.Second))
	return nil
}

// Close implement the Close() function of the service interface
func (c *Controller) Close() error {
	if c.tag != "" {
		limiter.DeleteLimiter(c.tag)
	}
	if c.onlineReportStopCh != nil {
		close(c.onlineReportStopCh)
	}
	if c.nodeInfoMonitorPeriodic != nil {
		c.nodeInfoMonitorPeriodic.Close()
	}
	if c.userReportPeriodic != nil {
		c.userReportPeriodic.Close()
	}
	if c.renewCertPeriodic != nil {
		c.renewCertPeriodic.Close()
	}
	if c.dynamicSpeedLimitPeriodic != nil {
		c.dynamicSpeedLimitPeriodic.Close()
	}
	if c.onlineIpReportPeriodic != nil {
		c.onlineIpReportPeriodic.Close()
	}
	if c.tag != "" {
		err := c.server.DelNode(c.tag)
		if err != nil {
			return fmt.Errorf("删除节点失败: %s", err)
		}
	}
	return nil
}

func (c *Controller) buildNodeTag(node *panel.NodeInfo) string {
	return fmt.Sprintf("[%s]-%s:%d", c.apiClient.APIHost, node.Type, node.Id)
}

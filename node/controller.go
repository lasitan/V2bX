package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/task"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	log "github.com/sirupsen/logrus"
)

type wsTrafficQueryRequest struct {
	Tag string `json:"tag,omitempty"`
	UID int    `json:"uid,omitempty"`
}

type wsTrafficQueryResponse struct {
	Tag     string             `json:"tag"`
	Traffic []panel.UserTraffic `json:"traffic"`
}

type Controller struct {
	server                    vCore.Core
	apiClient                 *panel.Client
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
	*conf.Options
}

func (c *Controller) handleWSTrafficQuery(req panel.WSRequest) (resp panel.WSResponse) {
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		return panel.WSResponse{StatusCode: http.StatusMethodNotAllowed, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("method not allowed")}
	}
	if req.Path != "/api/v1/server/UniProxy/traffic/query" {
		return panel.WSResponse{StatusCode: http.StatusNotFound, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("not found")}
	}

	q := wsTrafficQueryRequest{}
	if len(req.Body) > 0 {
		_ = json.Unmarshal(req.Body, &q)
	}
	if q.Tag != "" && q.Tag != c.tag {
		return panel.WSResponse{StatusCode: http.StatusBadRequest, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("tag mismatch")}
	}

	traffic, err := c.server.GetUserTrafficSlice(c.tag, false)
	if err != nil {
		return panel.WSResponse{StatusCode: http.StatusInternalServerError, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte(err.Error())}
	}
	if q.UID != 0 {
		filtered := make([]panel.UserTraffic, 0, 1)
		for _, t := range traffic {
			if t.UID == q.UID {
				filtered = append(filtered, t)
				break
			}
		}
		traffic = filtered
	}

	out := wsTrafficQueryResponse{Tag: c.tag, Traffic: traffic}
	b, err := json.Marshal(out)
	if err != nil {
		return panel.WSResponse{StatusCode: http.StatusInternalServerError, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte(err.Error())}
	}
	return panel.WSResponse{StatusCode: http.StatusOK, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: b}
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

	node, err := c.apiClient.GetNodeInfo()
	if err != nil {
		return fmt.Errorf("获取节点信息失败: %s", err)
	}
	// Update user
	c.userList, err = c.apiClient.GetUserList()
	if err != nil {
		return fmt.Errorf("获取用户列表失败: %s", err)
	}
	if len(c.userList) == 0 {
		return errors.New("添加用户失败: 面板未返回任何用户")
	}
	c.aliveMap, err = c.apiClient.GetUserAlive()
	if err != nil {
		return fmt.Errorf("获取用户在线列表失败: %s", err)
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
	c.apiClient.SetWSHandler(c.handleWSTrafficQuery)
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

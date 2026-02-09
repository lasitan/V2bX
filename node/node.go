package node

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vCore "github.com/InazumaV/V2bX/core"
	log "github.com/sirupsen/logrus"
)

type Node struct {
	controllers []*Controller
	closeCh     chan struct{}
	wg          sync.WaitGroup
}

func New() *Node {
	return &Node{}
}

func (n *Node) Start(nodes []conf.NodeConfig, core vCore.Core) error {
	if n.closeCh != nil {
		close(n.closeCh)
		n.wg.Wait()
		n.closeCh = nil
	}

	n.closeCh = make(chan struct{})
	n.controllers = make([]*Controller, 0, len(nodes))

	for i := range nodes {
		p, err := panel.New(&nodes[i].ApiConfig)
		if err != nil {
			log.WithFields(log.Fields{
				"api_host": nodes[i].ApiConfig.APIHost,
				"node_type": nodes[i].ApiConfig.NodeType,
				"node_id":   nodes[i].ApiConfig.NodeID,
				"err":       err,
			}).Error("面板客户端初始化失败，已跳过该节点")
			continue
		}
		c := NewController(core, p, &nodes[i].Options)
		n.controllers = append(n.controllers, c)

		apiHost := nodes[i].ApiConfig.APIHost
		nodeType := nodes[i].ApiConfig.NodeType
		nodeID := nodes[i].ApiConfig.NodeID

		wsMode := "default"
		wsEndpoint := ""
		if nodes[i].ApiConfig.WSURL != "" {
			wsMode = "ws_url"
			if u, err := url.Parse(nodes[i].ApiConfig.WSURL); err == nil {
				base := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}
				wsEndpoint = base.String()
			} else {
				wsEndpoint = nodes[i].ApiConfig.WSURL
			}
		} else {
			if nodes[i].ApiConfig.WSScheme != "" || nodes[i].ApiConfig.WSHost != "" || nodes[i].ApiConfig.WSPort > 0 {
				wsMode = "ws_parts"
			}
			apiBase, err := url.Parse(apiHost)
			if err == nil {
				scheme := "ws"
				if strings.EqualFold(apiBase.Scheme, "https") {
					scheme = "wss"
				}
				if nodes[i].ApiConfig.WSScheme != "" {
					scheme = nodes[i].ApiConfig.WSScheme
				}
				host := apiBase.Hostname()
				if nodes[i].ApiConfig.WSHost != "" {
					host = nodes[i].ApiConfig.WSHost
				}
				port := 51821
				if nodes[i].ApiConfig.WSPort > 0 {
					port = nodes[i].ApiConfig.WSPort
				}
				wsEndpoint = fmt.Sprintf("%s://%s:%d", scheme, host, port)
			}
		}

		log.WithFields(log.Fields{
			"api_host": apiHost,
			"ws_mode":  wsMode,
			"ws":       wsEndpoint,
			"node_type": nodeType,
			"node_id":   nodeID,
		}).Info("面板端点")

		n.wg.Add(1)
		go func(ctrl *Controller) {
			defer n.wg.Done()
			id := fmt.Sprintf("%s-%s-%d", apiHost, nodeType, nodeID)
			retry := 10 * time.Second
			for {
				err := ctrl.Start()
				if err == nil {
					log.WithField("node", id).Info("节点控制器启动成功")
					return
				}
				log.WithFields(log.Fields{"node": id, "err": err}).Error("节点控制器启动失败，将重试")
				select {
				case <-n.closeCh:
					return
				case <-time.After(retry):
				}
				if retry < 60*time.Second {
					retry *= 2
					if retry > 60*time.Second {
						retry = 60 * time.Second
					}
				}
			}
		}(c)
	}

	// Do not fail the whole process when a single node is misconfigured or panel is temporarily unreachable.
	return nil
}

func (n *Node) Close() {
	if n.closeCh != nil {
		close(n.closeCh)
		n.wg.Wait()
		n.closeCh = nil
	}
	for _, c := range n.controllers {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil {
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).Error("关闭节点控制器失败")
		}
	}
	n.controllers = nil
}

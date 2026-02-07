package node

import (
	"fmt"
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
			}).Error("panel client init failed, skip this node")
			continue
		}
		c := NewController(core, p, &nodes[i].Options)
		n.controllers = append(n.controllers, c)

		apiHost := nodes[i].ApiConfig.APIHost
		nodeType := nodes[i].ApiConfig.NodeType
		nodeID := nodes[i].ApiConfig.NodeID
		n.wg.Add(1)
		go func(ctrl *Controller) {
			defer n.wg.Done()
			id := fmt.Sprintf("%s-%s-%d", apiHost, nodeType, nodeID)
			retry := 10 * time.Second
			for {
				err := ctrl.Start()
				if err == nil {
					log.WithField("node", id).Info("node controller started")
					return
				}
				log.WithFields(log.Fields{"node": id, "err": err}).Error("node controller start failed, will retry")
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
			log.WithFields(log.Fields{"tag": c.tag, "err": err}).Error("close node controller failed")
		}
	}
	n.controllers = nil
}

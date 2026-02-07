package panel

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"

	"github.com/InazumaV/V2bX/conf"
	"github.com/go-resty/resty/v2"
)

// Panel is the interface for different panel's api.

type Client struct {
	client           *resty.Client
	APIHost          string
	Token            string
	NodeType         string
	NodeId           int
	timeout          time.Duration
	queryParams      map[string]string
	wsState          int32
	wsLastFailUnixNs int64
	wsConnMu         sync.Mutex
	wsConn           *websocket.Conn
	nodeEtag         string
	userEtag         string
	responseBodyHash string
	UserList         *UserListBody
	AliveMap         *AliveMap
}

func New(c *conf.ApiConfig) (*Client, error) {
	client := resty.New()
	client.SetRetryCount(3)
	var timeout time.Duration
	if c.Timeout > 0 {
		timeout = time.Duration(c.Timeout) * time.Second
		client.SetTimeout(timeout)
	} else {
		timeout = 5 * time.Second
		client.SetTimeout(timeout)
	}
	client.OnError(func(req *resty.Request, err error) {
		var v *resty.ResponseError
		if errors.As(err, &v) {
			// v.Response contains the last response from the server
			// v.Err contains the original error
			logrus.Error(v.Err)
		}
	})
	client.SetBaseURL(c.APIHost)
	// Check node type
	c.NodeType = strings.ToLower(c.NodeType)
	switch c.NodeType {
	case "v2ray":
		c.NodeType = "vmess"
	case
		"vmess",
		"trojan",
		"shadowsocks",
		"hysteria",
		"hysteria2",
		"tuic",
		"anytls",
		"vless":
	default:
		return nil, fmt.Errorf("unsupported Node type: %s", c.NodeType)
	}
	// set params
	queryParams := map[string]string{
		"node_type": c.NodeType,
		"node_id":   strconv.Itoa(c.NodeID),
		"token":     c.Key,
	}
	client.SetQueryParams(queryParams)
	return &Client{
		client:   client,
		Token:    c.Key,
		APIHost:  c.APIHost,
		NodeType: c.NodeType,
		NodeId:   c.NodeID,
		timeout:  timeout,
		queryParams: func() map[string]string {
			m := make(map[string]string, len(queryParams))
			for k, v := range queryParams {
				m[k] = v
			}
			return m
		}(),
		wsState:  int32(wsStateUnknown),
		UserList: &UserListBody{},
		AliveMap: &AliveMap{},
	}, nil
}

func (c *Client) disableWS() {
	atomic.StoreInt32(&c.wsState, int32(wsStateUnavailable))
	c.wsConnMu.Lock()
	c.closeWSConnLocked()
	c.wsConnMu.Unlock()
}

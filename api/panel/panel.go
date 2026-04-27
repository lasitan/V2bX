package panel

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
	wsTransportLogOnce   sync.Once
	httpTransportLogOnce sync.Once
	timeout          time.Duration
	queryParams      map[string]string
	nodeEtag         string
	userEtag         string
	responseBodyHash string
	UserList         *UserListBody
	AliveMap         *AliveMap
}

func New(c *conf.ApiConfig) (*Client, error) {
	client := resty.New()
	client.SetRetryCount(3)
	client.SetRetryWaitTime(300 * time.Millisecond)
	client.SetRetryMaxWaitTime(2 * time.Second)
	client.AddRetryCondition(func(resp *resty.Response, err error) bool {
		if err != nil {
			return true
		}
		if resp == nil {
			return true
		}
		return resp.StatusCode() >= http.StatusInternalServerError || resp.StatusCode() == http.StatusTooManyRequests
	})
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
	client.SetTransport(&http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	})
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
		UserList: &UserListBody{},
		AliveMap: &AliveMap{},
	}, nil
}

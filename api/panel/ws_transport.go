package panel

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

func (c *Client) doRequest(method, path string, headers map[string]string, body []byte) (status int, respHeaders map[string][]string, respBody []byte, usedWS bool, err error) {
	r := c.client.R()
	// Reporting endpoints can be slower than config/list pulls; use a larger request timeout
	// and retry budget to avoid transient panel latency causing cascading failures.
	if strings.EqualFold(method, http.MethodPost) &&
		(path == "/api/v1/server/UniProxy/push" || path == "/api/v1/server/UniProxy/alive") {
		timeout := 45 * time.Second
		if c.timeout > 0 && c.timeout > timeout {
			timeout = c.timeout
		}
		r.SetTimeout(timeout)
		r.SetRetryCount(5)
	} else if c.timeout > 0 {
		r.SetTimeout(c.timeout)
	}
	for k, v := range headers {
		r.SetHeader(k, v)
	}
	if body != nil {
		r.SetBody(body)
	}

	var resp *resty.Response
	switch strings.ToUpper(method) {
	case http.MethodGet:
		resp, err = r.Get(path)
	case http.MethodPost:
		resp, err = r.Post(path)
	default:
		return 0, nil, nil, false, fmt.Errorf("unsupported method: %s", method)
	}
	if err != nil {
		if hc := c.client.GetClient(); hc != nil {
			hc.CloseIdleConnections()
		}
		return 0, nil, nil, false, err
	}
	if resp == nil {
		return 0, nil, nil, false, fmt.Errorf("received nil response")
	}
	c.httpTransportLogOnce.Do(func() {
		logrus.WithFields(logrus.Fields{
			"api_host":  c.APIHost,
			"node_type": c.NodeType,
			"node_id":   c.NodeId,
			"transport": "http",
			"method":    strings.ToUpper(method),
			"path":      path,
			"status":    resp.StatusCode(),
		}).Debug("面板通信使用 HTTP")
	})
	return resp.StatusCode(), resp.Header(), resp.Body(), false, nil
}

func (c *Client) checkResponseRaw(path string, status int, body []byte, err error) error {
	if err != nil {
		return fmt.Errorf("request %s failed: %s", c.assembleURL(path), err)
	}
	if status >= 400 {
		return fmt.Errorf("request %s failed: %s", c.assembleURL(path), string(body))
	}
	return nil
}

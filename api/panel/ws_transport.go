package panel

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

func (c *Client) doRequest(method, path string, headers map[string]string, body []byte) (status int, respHeaders map[string][]string, respBody []byte, usedWS bool, err error) {
	isReportPath := strings.EqualFold(method, http.MethodPost) &&
		(path == "/api/v1/server/UniProxy/push" || path == "/api/v1/server/UniProxy/alive")

	maxAttempts := 1
	reqTimeout := c.timeout
	if reqTimeout <= 0 {
		reqTimeout = 5 * time.Second
	}
	if isReportPath {
		maxAttempts = 5
		if reqTimeout < 45*time.Second {
			reqTimeout = 45 * time.Second
		}
	}

	var resp *resty.Response
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		r := c.client.R()
		for k, v := range headers {
			r.SetHeader(k, v)
		}
		if body != nil {
			r.SetBody(body)
		}
		ctx, cancel := context.WithTimeout(context.Background(), reqTimeout)
		r.SetContext(ctx)
		switch strings.ToUpper(method) {
		case http.MethodGet:
			resp, err = r.Get(path)
		case http.MethodPost:
			resp, err = r.Post(path)
		default:
			cancel()
			return 0, nil, nil, false, fmt.Errorf("unsupported method: %s", method)
		}
		cancel()

		shouldRetry := false
		if err != nil {
			shouldRetry = true
			if hc := c.client.GetClient(); hc != nil {
				hc.CloseIdleConnections()
			}
		} else if resp != nil && (resp.StatusCode() >= http.StatusInternalServerError || resp.StatusCode() == http.StatusTooManyRequests) {
			shouldRetry = attempt < maxAttempts
		}
		if !shouldRetry || attempt == maxAttempts {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err != nil {
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

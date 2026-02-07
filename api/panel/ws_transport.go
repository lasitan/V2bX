package panel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"encoding/json/v2"

	"github.com/gorilla/websocket"
	"github.com/go-resty/resty/v2"
)

type wsState int32

const (
	wsStateUnknown wsState = iota
	wsStateAvailable
	wsStateUnavailable
)

type wsRequest struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

type wsResponse struct {
	StatusCode int                 `json:"status"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
}

func (c *Client) closeWSConnLocked() {
	if c.wsConn != nil {
		_ = c.wsConn.Close()
		c.wsConn = nil
	}
}

func (c *Client) ensureWSConnLocked(ctx context.Context, headers map[string]string, path string) error {
	if c.wsConn != nil {
		return nil
	}
	wsURL, err := c.wsURL(path)
	if err != nil {
		return err
	}

	dialer := websocket.Dialer{}
	if strings.HasPrefix(wsURL, "wss://") {
		dialer.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, h)
	if err != nil {
		return err
	}
	c.wsConn = conn
	return nil
}

func (c *Client) wsEnabled() bool {
	state := wsState(atomic.LoadInt32(&c.wsState))
	if state != wsStateUnavailable {
		return true
	}
	lastFail := atomic.LoadInt64(&c.wsLastFailUnixNs)
	if lastFail <= 0 {
		return false
	}
	// Retry WS periodically after a failure.
	if time.Since(time.Unix(0, lastFail)) >= 60*time.Second {
		atomic.StoreInt32(&c.wsState, int32(wsStateUnknown))
		return true
	}
	return false
}

func (c *Client) setWsState(s wsState) {
	atomic.StoreInt32(&c.wsState, int32(s))
	if s == wsStateUnavailable {
		atomic.StoreInt64(&c.wsLastFailUnixNs, time.Now().UnixNano())
	}
}

func (c *Client) wsURL(path string) (string, error) {
	// Optional override:
	// - If WSURL is provided, it must include scheme and host (port optional).
	// - Otherwise, WsScheme/WsHost/WsPort can be provided partially.
	wsu := &url.URL{Path: path}
	if c.WSURL != "" {
		base, err := url.Parse(c.WSURL)
		if err != nil {
			return "", err
		}
		wsu.Scheme = base.Scheme
		wsu.Host = base.Host
		// If configured URL includes a path prefix, respect it by joining with requested path.
		if base.Path != "" && base.Path != "/" {
			wsu.Path = strings.TrimRight(base.Path, "/") + path
		}
	} else {
		apiBase, err := url.Parse(c.APIHost)
		if err != nil {
			return "", err
		}
		hostname := apiBase.Hostname()
		if hostname == "" {
			return "", fmt.Errorf("invalid ApiHost: %s", c.APIHost)
		}
		scheme := "ws"
		if strings.EqualFold(apiBase.Scheme, "https") {
			scheme = "wss"
		}
		if c.WSScheme != "" {
			scheme = c.WSScheme
		}
		host := hostname
		if c.WSHost != "" {
			host = c.WSHost
		}
		port := 51821
		if c.WSPort > 0 {
			port = c.WSPort
		}
		wsu.Scheme = scheme
		wsu.Host = fmt.Sprintf("%s:%d", host, port)
	}

	q := wsu.Query()
	for k, v := range c.queryParams {
		q.Set(k, v)
	}
	wsu.RawQuery = q.Encode()
	return wsu.String(), nil
}

func (c *Client) doWS(ctx context.Context, method, path string, headers map[string]string, body []byte) (*wsResponse, error) {
	req := wsRequest{
		Method:  method,
		Path:    path,
		Headers: make(map[string][]string, len(headers)),
		Body:    body,
	}
	for k, v := range headers {
		req.Headers[k] = []string{v}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	c.wsConnMu.Lock()
	defer c.wsConnMu.Unlock()

	tryOnce := func() ([]byte, error) {
		if err := c.ensureWSConnLocked(ctx, headers, path); err != nil {
			return nil, err
		}
		if d, ok := ctx.Deadline(); ok {
			_ = c.wsConn.SetWriteDeadline(d)
			_ = c.wsConn.SetReadDeadline(d)
		}
		if err := c.wsConn.WriteMessage(websocket.TextMessage, payload); err != nil {
			c.closeWSConnLocked()
			return nil, err
		}
		_, respPayload, err := c.wsConn.ReadMessage()
		if err != nil {
			c.closeWSConnLocked()
			return nil, err
		}
		if _, ok := ctx.Deadline(); ok {
			_ = c.wsConn.SetWriteDeadline(time.Time{})
			_ = c.wsConn.SetReadDeadline(time.Time{})
		}
		return respPayload, nil
	}

	respPayload, err := tryOnce()
	if err != nil {
		// Reconnect once and retry the same request.
		respPayload, err = tryOnce()
		if err != nil {
			return nil, err
		}
	}

	var resp wsResponse
	if err := json.Unmarshal(respPayload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) doRequest(method, path string, headers map[string]string, body []byte) (status int, respHeaders map[string][]string, respBody []byte, usedWS bool, err error) {
	if c.wsEnabled() {
		ctx := context.Background()
		if c.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.timeout)
			defer cancel()
		}
		resp, wsErr := c.doWS(ctx, method, path, headers, body)
		if wsErr == nil {
			c.setWsState(wsStateAvailable)
			return resp.StatusCode, resp.Headers, resp.Body, true, nil
		}
		c.setWsState(wsStateUnavailable)
	}

	r := c.client.R()
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
		return 0, nil, nil, false, err
	}
	if resp == nil {
		return 0, nil, nil, false, fmt.Errorf("received nil response")
	}
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

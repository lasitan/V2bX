package panel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"encoding/json"

	"github.com/gorilla/websocket"
	"github.com/go-resty/resty/v2"
	"github.com/sirupsen/logrus"
)

type wsState int32

const (
	wsStateUnknown wsState = iota
	wsStateAvailable
	wsStateUnavailable
)


type WSRequest struct {
	Id      int64               `json:"id,omitempty"`
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}


type WSResponse struct {
	Id         int64               `json:"id,omitempty"`
	StatusCode int                 `json:"status"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Body       []byte              `json:"body,omitempty"`
}

func (c *Client) closeWSConnLocked() {
	if c.wsConn != nil {
		_ = c.wsConn.Close()
		c.wsConn = nil
	}
	c.wsPendingMu.Lock()
	for id, ch := range c.wsPending {
		delete(c.wsPending, id)
		select {
		case ch <- wsPendingResult{resp: nil, err: errors.New("ws connection closed")}:
		default:
		}
		close(ch)
	}
	c.wsPendingMu.Unlock()
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
	go c.wsReadLoop(conn)
	return nil
}

type wsMessage struct {
	Id      int64               `json:"id,omitempty"`
	Method  string              `json:"method,omitempty"`
	Path    string              `json:"path,omitempty"`
	Status  int                 `json:"status,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

func (c *Client) wsReadLoop(conn *websocket.Conn) {
	for {
		mt, payload, err := conn.ReadMessage()
		if err != nil {
			c.wsConnMu.Lock()
			if c.wsConn == conn {
				c.closeWSConnLocked()
				c.setWsState(wsStateUnavailable)
			}
			c.wsConnMu.Unlock()
			return
		}
		if mt != websocket.TextMessage && mt != websocket.BinaryMessage {
			continue
		}
		var msg wsMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		// Response path: dispatch to pending request.
		if msg.Method == "" {
			resp := &WSResponse{Id: msg.Id, StatusCode: msg.Status, Headers: msg.Headers, Body: msg.Body}
			if c.dispatchWSPending(resp) {
				continue
			}
			continue
		}

		// Request path: handle server-initiated request.
		c.wsHandlerMu.RLock()
		h := c.wsHandler
		c.wsHandlerMu.RUnlock()
		if h == nil {
			_ = c.wsWriteResponse(conn, &WSResponse{Id: msg.Id, StatusCode: 404, Headers: map[string][]string{"Content-Type": {"text/plain"}}, Body: []byte("no handler")})
			continue
		}
		resp := h(WSRequest{Id: msg.Id, Method: msg.Method, Path: msg.Path, Headers: msg.Headers, Body: msg.Body})
		resp.Id = msg.Id
		_ = c.wsWriteResponse(conn, &resp)
	}
}

func (c *Client) dispatchWSPending(resp *WSResponse) bool {
	c.wsPendingMu.Lock()
	defer c.wsPendingMu.Unlock()
	if resp.Id != 0 {
		if ch, ok := c.wsPending[resp.Id]; ok {
			delete(c.wsPending, resp.Id)
			ch <- wsPendingResult{resp: resp, err: nil}
			close(ch)
			return true
		}
		return false
	}
	// Backward compatibility: if no id, deliver to the only pending request.
	if len(c.wsPending) == 1 {
		for id, ch := range c.wsPending {
			delete(c.wsPending, id)
			ch <- wsPendingResult{resp: resp, err: nil}
			close(ch)
			return true
		}
	}
	return false
}

func (c *Client) wsWriteResponse(conn *websocket.Conn, resp *WSResponse) error {
	payload, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	c.wsWriteMu.Lock()
	defer c.wsWriteMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, payload)
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


func (c *Client) doWS(ctx context.Context, method, path string, headers map[string]string, body []byte) (*WSResponse, error) {
	id := atomic.AddInt64(&c.wsNextID, 1)
	req := WSRequest{Id: id, Method: method, Path: path, Headers: make(map[string][]string, len(headers)), Body: body}
	for k, v := range headers {
		req.Headers[k] = []string{v}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	resultCh := make(chan wsPendingResult, 1)
	c.wsPendingMu.Lock()
	c.wsPending[id] = resultCh
	c.wsPendingMu.Unlock()

	tryOnce := func() error {
		c.wsConnMu.Lock()
		defer c.wsConnMu.Unlock()
		if err := c.ensureWSConnLocked(ctx, headers, path); err != nil {
			return err
		}
		if d, ok := ctx.Deadline(); ok {
			_ = c.wsConn.SetWriteDeadline(d)
		}
		c.wsWriteMu.Lock()
		err := c.wsConn.WriteMessage(websocket.TextMessage, payload)
		c.wsWriteMu.Unlock()
		if err != nil {
			c.closeWSConnLocked()
			return err
		}
		if _, ok := ctx.Deadline(); ok {
			_ = c.wsConn.SetWriteDeadline(time.Time{})
		}
		return nil
	}

	if err := tryOnce(); err != nil {
		// Reconnect once and retry the same request.
		if err := tryOnce(); err != nil {
			c.wsPendingMu.Lock()
			delete(c.wsPending, id)
			c.wsPendingMu.Unlock()
			close(resultCh)
			return nil, err
		}
	}

	select {
	case r := <-resultCh:
		return r.resp, r.err
	case <-ctx.Done():
		c.wsPendingMu.Lock()
		delete(c.wsPending, id)
		c.wsPendingMu.Unlock()
		close(resultCh)
		return nil, ctx.Err()
	}
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
			c.wsTransportLogOnce.Do(func() {
				logrus.WithFields(logrus.Fields{
					"api_host":  c.APIHost,
					"node_type": c.NodeType,
					"node_id":   c.NodeId,
					"transport": "ws",
					"method":    strings.ToUpper(method),
					"path":      path,
					"status":    resp.StatusCode,
				}).Info("面板通信使用 WS")
			})
			return resp.StatusCode, resp.Headers, resp.Body, true, nil
		}
		c.setWsState(wsStateUnavailable)
		logrus.WithFields(logrus.Fields{
			"api_host":  c.APIHost,
			"node_type": c.NodeType,
			"node_id":   c.NodeId,
			"transport": "ws",
			"method":    strings.ToUpper(method),
			"path":      path,
			"err":       wsErr,
		}).Warn("面板 WS 通信失败，回退到 HTTP")
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

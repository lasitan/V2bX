package sing

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type wsMuxKey struct {
	listenIP string
	port     uint16
}

type wsRouteKey struct {
	host string
	path string
}

type wsRoute struct {
	backendAddr string
	tag         string
}

type wsMuxManager struct {
	mu      sync.Mutex
	servers map[wsMuxKey]*wsMuxServer
	tagMap  map[string]wsMuxTagBinding
}

type wsMuxTagBinding struct {
	key  wsMuxKey
	route wsRouteKey
}

type wsMuxServer struct {
	key wsMuxKey

	mu     sync.RWMutex
	routes map[wsRouteKey]wsRoute

	ln    net.Listener
	wg    sync.WaitGroup
	close chan struct{}
}

func newWsMuxManager() *wsMuxManager {
	return &wsMuxManager{
		servers: make(map[wsMuxKey]*wsMuxServer),
		tagMap:  make(map[string]wsMuxTagBinding),
	}
}

func (m *wsMuxManager) CloseAll() {
	m.mu.Lock()
	servers := make([]*wsMuxServer, 0, len(m.servers))
	for _, s := range m.servers {
		servers = append(servers, s)
	}
	m.servers = make(map[wsMuxKey]*wsMuxServer)
	m.tagMap = make(map[string]wsMuxTagBinding)
	m.mu.Unlock()

	for _, s := range servers {
		s.Close()
	}
}

func (m *wsMuxManager) Ensure(listenIP string, port uint16) (*wsMuxServer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := wsMuxKey{listenIP: listenIP, port: port}
	if s, ok := m.servers[key]; ok {
		return s, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(listenIP, fmt.Sprint(port)))
	if err != nil {
		return nil, err
	}
	s := &wsMuxServer{
		key:    key,
		routes: make(map[wsRouteKey]wsRoute),
		ln:     ln,
		close:  make(chan struct{}),
	}
	m.servers[key] = s
	s.wg.Add(1)
	go s.serve()
	log.WithFields(log.Fields{"listen_ip": listenIP, "port": port}).Info("ws mux started")
	return s, nil
}

func (m *wsMuxManager) Register(tag string, listenIP string, port uint16, host string, path string, backendAddr string) error {
	if path == "" {
		path = "/"
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	key := wsMuxKey{listenIP: listenIP, port: port}
	s, ok := m.servers[key]
	if !ok {
		return errors.New("ws mux server not found")
	}
	rKey := wsRouteKey{host: normalizeHost(host), path: path}

	s.mu.Lock()
	if old, exists := s.routes[rKey]; exists {
		s.mu.Unlock()
		return fmt.Errorf("ws mux route conflict host=%s path=%s already used by tag=%s", rKey.host, rKey.path, old.tag)
	}
	s.routes[rKey] = wsRoute{backendAddr: backendAddr, tag: tag}
	s.mu.Unlock()

	m.tagMap[tag] = wsMuxTagBinding{key: key, route: rKey}
	log.WithFields(log.Fields{"listen_ip": listenIP, "port": port, "host": rKey.host, "path": rKey.path, "backend": backendAddr, "tag": tag}).Info("ws mux route registered")
	return nil
}

func (m *wsMuxManager) Unregister(tag string) {
	m.mu.Lock()
	binding, ok := m.tagMap[tag]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.tagMap, tag)
	s, ok := m.servers[binding.key]
	m.mu.Unlock()
	if !ok {
		return
	}

	s.mu.Lock()
	delete(s.routes, binding.route)
	remaining := len(s.routes)
	s.mu.Unlock()
	log.WithFields(log.Fields{"listen_ip": binding.key.listenIP, "port": binding.key.port, "host": binding.route.host, "path": binding.route.path, "tag": tag}).Info("ws mux route unregistered")

	if remaining == 0 {
		m.mu.Lock()
		delete(m.servers, binding.key)
		m.mu.Unlock()
		s.Close()
		log.WithFields(log.Fields{"listen_ip": binding.key.listenIP, "port": binding.key.port}).Info("ws mux stopped")
	}
}

func (s *wsMuxServer) Close() {
	_ = s.ln.Close()
	select {
	case <-s.close:
		return
	default:
		close(s.close)
	}
	s.wg.Wait()
}

func (s *wsMuxServer) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.close:
				return
			default:
				continue
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(c)
		}()
	}
}

func (s *wsMuxServer) handleConn(c net.Conn) {
	defer c.Close()

	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReaderSize(c, 64*1024)
	head, host, path, err := readHTTPHeader(br)
	if err != nil {
		return
	}
	_ = c.SetReadDeadline(time.Time{})

	host = normalizeHost(host)
	path = normalizePath(path)

	s.mu.RLock()
	route, ok := s.routes[wsRouteKey{host: host, path: path}]
	if !ok {
		route, ok = s.routes[wsRouteKey{host: "*", path: path}]
	}
	if !ok {
		route, ok = s.routes[wsRouteKey{host: host, path: "*"}]
	}
	if !ok {
		route, ok = s.routes[wsRouteKey{host: "*", path: "*"}]
	}
	s.mu.RUnlock()
	if !ok {
		return
	}

	backend, err := net.DialTimeout("tcp", route.backendAddr, 10*time.Second)
	if err != nil {
		return
	}
	defer backend.Close()

	_, err = backend.Write(head)
	if err != nil {
		return
	}

	errCh := make(chan error, 2)
	go func() {
		e := relayStream(backend, br)
		closeWrite(backend)
		errCh <- e
	}()
	go func() {
		e := relayStream(c, backend)
		closeWrite(c)
		errCh <- e
	}()
	err1 := <-errCh
	err2 := <-errCh
	if relayErrorUnexpected(err1) && relayErrorUnexpected(err2) {
		log.WithFields(log.Fields{
			"listen_ip": s.key.listenIP,
			"port":      s.key.port,
			"host":      host,
			"path":      path,
			"backend":   route.backendAddr,
			"err1":      err1,
			"err2":      err2,
		}).Debug("ws mux relay closed with errors")
	}
}

// relayStream avoids io.Copy's splice fast-path on Linux kernels where
// long-running websocket relay may hit runtime-invalid-argument crashes.
func relayStream(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			written := 0
			for written < nr {
				nw, ew := dst.Write(buf[written:nr])
				if nw > 0 {
					written += nw
				}
				if ew != nil {
					return ew
				}
				if nw == 0 {
					return io.ErrShortWrite
				}
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return nil
			}
			return er
		}
	}
}

func closeWrite(conn net.Conn) {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		_ = tcpConn.CloseWrite()
	}
}

func relayErrorUnexpected(err error) bool {
	if err == nil || errors.Is(err, io.EOF) {
		return false
	}
	return !errors.Is(err, net.ErrClosed)
}

func readHTTPHeader(br *bufio.Reader) (raw []byte, host string, path string, err error) {
	var buf []byte
	for {
		line, e := br.ReadBytes('\n')
		if e != nil {
			return nil, "", "", e
		}
		buf = append(buf, line...)
		if len(buf) > 64*1024 {
			return nil, "", "", errors.New("header too large")
		}
		if strings.Contains(string(buf), "\r\n\r\n") {
			break
		}
	}

	req, e := http.ReadRequest(bufio.NewReader(strings.NewReader(string(buf))))
	if e != nil {
		return nil, "", "", e
	}
	host = req.Host
	path = req.URL.Path
	if path == "" {
		path = "/"
	}
	return buf, host, path, nil
}

func normalizeHost(h string) string {
	if h == "" {
		return ""
	}
	if strings.Contains(h, ":") {
		if host, _, err := net.SplitHostPort(h); err == nil {
			return strings.ToLower(host)
		}
	}
	return strings.ToLower(h)
}

func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") && p != "*" {
		return "/" + p
	}
	return p
}

func loopbackFor(listenIP string) (string, error) {
	addr, err := netip.ParseAddr(listenIP)
	if err != nil {
		return "", err
	}
	if addr.Is6() {
		return "::1", nil
	}
	return "127.0.0.1", nil
}

func allocatePort(listenIP string) (uint16, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(listenIP, "0"))
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("unexpected addr type")
	}
	return uint16(addr.Port), nil
}

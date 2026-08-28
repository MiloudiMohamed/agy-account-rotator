package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MiloudiMohamed/agy-account-rotator/internal/detect"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/google"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/history"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/notify"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/rotate"
	"github.com/MiloudiMohamed/agy-account-rotator/internal/store"
)

// Server is the transparent in-flight request proxy server.
type Server struct {
	Store        *store.Store
	Engine       *rotate.Engine
	CertManager  *CertManager
	Port         int
	IdleTimeout  time.Duration
	listener     net.Listener
	httpServer   *http.Server
	startedAt    time.Time
	lastActivity time.Time
	activityMu   sync.Mutex
	requestCount uint64
	activeConns  int64
	upstreamHTTP *http.Client
	shutdownChan chan struct{}
}

// HealthResponse represents the payload returned by /healthz.
type HealthResponse struct {
	Status       string `json:"status"`
	Version      string `json:"version"`
	ActiveEmail  string `json:"active_email"`
	Mode         string `json:"mode"`
	Port         int    `json:"port"`
	PID          int    `json:"pid"`
	Uptime       string `json:"uptime"`
	RequestCount uint64 `json:"request_count"`
}

// NewServer initializes a new proxy server.
func NewServer(s *store.Store, eng *rotate.Engine, cm *CertManager, port int, idleTimeout time.Duration) *Server {
	if port <= 0 {
		port = 8999
	}
	if idleTimeout <= 0 {
		idleTimeout = 60 * time.Minute
	}

	transport := &http.Transport{
		Proxy:                 nil, // direct upstream
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	srv := &Server{
		Store:        s,
		Engine:       eng,
		CertManager:  cm,
		Port:         port,
		IdleTimeout:  idleTimeout,
		startedAt:    time.Now().UTC(),
		lastActivity: time.Now().UTC(),
		upstreamHTTP: &http.Client{
			Transport: transport,
			Timeout:   0, // streaming responses must not time out
		},
		shutdownChan: make(chan struct{}),
	}

	return srv
}

// Start begins listening on the configured port and blocks until shutdown or error.
func (s *Server) Start() error {
	addr := fmt.Sprintf("127.0.0.1:%d", s.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}
	s.listener = ln

	// Write PID file
	pidData := []byte(strconv.Itoa(os.Getpid()) + "\n")
	_ = store.WriteFileAtomic(s.Store.ProxyPIDPath(), pidData, 0o644)

	s.httpServer = &http.Server{
		Handler: s,
	}

	// Start idle watchdog
	go s.runIdleWatchdog()

	return s.httpServer.Serve(ln)
}

// Shutdown gracefully stops the proxy server.
func (s *Server) Shutdown(ctx context.Context) error {
	select {
	case <-s.shutdownChan:
		// already shutting down
	default:
		close(s.shutdownChan)
	}

	_ = os.Remove(s.Store.ProxyPIDPath())

	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) recordActivity() {
	s.activityMu.Lock()
	s.lastActivity = time.Now().UTC()
	s.activityMu.Unlock()
	atomic.AddUint64(&s.requestCount, 1)
}

func (s *Server) runIdleWatchdog() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.shutdownChan:
			return
		case <-ticker.C:
			s.activityMu.Lock()
			idle := time.Since(s.lastActivity)
			active := atomic.LoadInt64(&s.activeConns)
			s.activityMu.Unlock()

			if active == 0 && idle > s.IdleTimeout {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.Shutdown(ctx)
				cancel()
				return
			}
		}
	}
}

// ServeHTTP routes HTTP requests: handles CONNECT tunneling, /healthz, or forward proxying.
func (s *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.recordActivity()

	if req.Method == http.MethodConnect {
		s.handleConnect(w, req)
		return
	}

	if req.URL.Path == "/healthz" {
		s.handleHealthz(w, req)
		return
	}

	if req.URL.Path == "/shutdown" && req.Method == http.MethodPost {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("shutting down\n"))
		go func() {
			time.Sleep(100 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.Shutdown(ctx)
		}()
		return
	}

	s.handleForwardRequest(w, req, req.URL.Host, req.URL.Scheme)
}

func (s *Server) handleHealthz(w http.ResponseWriter, req *http.Request) {
	st := s.Store.LoadState()
	cfg := s.Store.LoadConfig()

	resp := HealthResponse{
		Status:       "ok",
		Version:      "0.1.0",
		ActiveEmail:  st.ActiveEmail,
		Mode:         cfg.Mode,
		Port:         s.Port,
		PID:          os.Getpid(),
		Uptime:       time.Since(s.startedAt).Round(time.Second).String(),
		RequestCount: atomic.LoadUint64(&s.requestCount),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func shouldInterceptHost(host string) bool {
	h := host
	if idx := strings.Index(h, ":"); idx != -1 {
		h = h[:idx]
	}
	h = strings.ToLower(h)
	return strings.Contains(h, "cloudcode-pa.googleapis.com") ||
		strings.Contains(h, "oauth2.googleapis.com") ||
		strings.Contains(h, "cloudcode")
}

func (s *Server) handleConnect(w http.ResponseWriter, req *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	targetHost := req.Host
	if !strings.Contains(targetHost, ":") {
		targetHost += ":443"
	}

	if shouldInterceptHost(targetHost) {
		atomic.AddInt64(&s.activeConns, 1)
		defer atomic.AddInt64(&s.activeConns, -1)
		s.handleInterceptedTLS(clientConn, targetHost)
	} else {
		s.handleDirectPipe(clientConn, targetHost)
	}
}

func (s *Server) handleDirectPipe(clientConn net.Conn, targetHost string) {
	defer clientConn.Close()

	destConn, err := net.DialTimeout("tcp", targetHost, 10*time.Second)
	if err != nil {
		return
	}
	defer destConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(destConn, clientConn)
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, destConn)
	}()

	wg.Wait()
}

type singleConnListener struct {
	conn      net.Conn
	done      chan struct{}
	delivered int32
}

func newSingleConnListener(c net.Conn) *singleConnListener {
	return &singleConnListener{
		conn: c,
		done: make(chan struct{}),
	}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if atomic.CompareAndSwapInt32(&l.delivered, 0, 1) {
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return l.conn.Close()
}

func (l *singleConnListener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func (s *Server) handleInterceptedTLS(clientConn net.Conn, targetHost string) {
	hostOnly := targetHost
	if idx := strings.Index(hostOnly, ":"); idx != -1 {
		hostOnly = hostOnly[:idx]
	}

	tlsConfig := s.CertManager.TLSConfig()
	tlsClient := tls.Server(clientConn, tlsConfig)
	if err := tlsClient.Handshake(); err != nil {
		clientConn.Close()
		return
	}

	ln := newSingleConnListener(tlsClient)
	defer ln.Close()

	// Serve HTTP on the intercepted TLS connection using standard http.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.recordActivity()
		s.handleForwardRequest(w, req, hostOnly, "https")
	})

	httpServer := &http.Server{
		Handler: handler,
	}

	_ = httpServer.Serve(ln)
}

func (s *Server) handleForwardRequest(w http.ResponseWriter, inReq *http.Request, hostOnly, scheme string) {
	if scheme == "" {
		scheme = "https"
	}
	if hostOnly == "" {
		hostOnly = inReq.Host
	}

	// Buffer request body for replay on 429
	var bodyBytes []byte
	if inReq.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(inReq.Body)
		inReq.Body.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	cfg := s.Store.LoadConfig()
	const maxRetries = 3

	for attempt := 0; attempt < maxRetries; attempt++ {
		st := s.Store.LoadState()
		activeEmail := st.ActiveEmail

		// If no active account is set, pick one
		if activeEmail == "" {
			if next, err := s.Engine.PickNext(); err == nil {
				_ = s.Engine.Activate(next)
				activeEmail = next.Email
			}
		}

		// Ensure token freshness for active account
		var accessToken string
		if activeEmail != "" {
			if acct, err := s.Store.GetAccount(activeEmail); err == nil {
				now := time.Now()
				exp := acct.File.Token.ExpiryTime()
				if exp.IsZero() || now.Add(60*time.Second).After(exp) {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					rr, rerr := google.Refresh(ctx, http.DefaultClient, acct.File.Token.RefreshToken)
					cancel()
					if rerr == nil {
						acct.File.Token.AccessToken = rr.AccessToken
						acct.File.Token.Expiry = google.ExpiryFrom(now, rr.ExpiresIn).Format(time.RFC3339)
						if rr.RefreshToken != "" {
							acct.File.Token.RefreshToken = rr.RefreshToken
						}
						_ = s.Store.SaveAccount(acct)
						_ = s.Store.WriteTokenFile(&acct.File)
					}
				}
				accessToken = acct.File.Token.AccessToken
			}
		}

		// Build upstream request
		targetURL := fmt.Sprintf("%s://%s%s", scheme, hostOnly, inReq.URL.RequestURI())
		outReq, err := http.NewRequestWithContext(inReq.Context(), inReq.Method, targetURL, bytes.NewReader(bodyBytes))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		outReq.Host = hostOnly

		// Copy headers (excluding hop-by-hop)
		for k, vv := range inReq.Header {
			if strings.EqualFold(k, "Proxy-Connection") || strings.EqualFold(k, "Proxy-Authorization") {
				continue
			}
			for _, v := range vv {
				outReq.Header.Add(k, v)
			}
		}

		// Inject fresh Bearer token
		if accessToken != "" {
			outReq.Header.Set("Authorization", "Bearer "+accessToken)
		}

		resp, err := s.upstreamHTTP.Do(outReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		// Check if response is rate-limited (HTTP 429, 529, or quota errors)
		isRateLimit := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529

		// If 429 and we can retry, switch account and loop
		if isRateLimit && attempt < maxRetries-1 {
			respBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			reason := detect.Classify(string(respBody))
			if reason == detect.None {
				reason = detect.QuotaExhausted
			}

			if activeEmail != "" {
				d, _ := s.Engine.ReportFailure(activeEmail, reason)
				if cfg.Notifications {
					notify.Send("agy-rotator", fmt.Sprintf("[%s] Quota hit on %s. Auto-cooling %s",
						reason, activeEmail, d.Truncate(time.Second)))
				}
			}

			// Pick next eligible account
			if next, err := s.Engine.PickNext(); err == nil && next.Email != activeEmail {
				_ = s.Engine.Activate(next)
				_ = history.Append(s.Store, history.Event{
					Type:   history.EventRotate,
					Email:  next.Email,
					Reason: fmt.Sprintf("in-flight auto-retry after %s on %s", reason, activeEmail),
				})
			}
			continue // retry with next account
		}

		// Stream response back to client in real-time
		defer resp.Body.Close()

		// Copy upstream headers to response writer (excluding hop-by-hop headers)
		for k, vv := range resp.Header {
			if strings.EqualFold(k, "Transfer-Encoding") ||
				strings.EqualFold(k, "Connection") ||
				strings.EqualFold(k, "Keep-Alive") ||
				strings.EqualFold(k, "Proxy-Authenticate") ||
				strings.EqualFold(k, "Proxy-Authorization") {
				continue
			}
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}

		w.WriteHeader(resp.StatusCode)

		// Real-time chunked flushing for SSE and token streams
		if flusher, ok := w.(http.Flusher); ok {
			buf := make([]byte, 2048)
			for {
				n, rerr := resp.Body.Read(buf)
				if n > 0 {
					_, werr := w.Write(buf[:n])
					flusher.Flush()
					if werr != nil {
						break
					}
				}
				if rerr != nil {
					break
				}
			}
		} else {
			_, _ = io.Copy(w, resp.Body)
		}

		return
	}
}

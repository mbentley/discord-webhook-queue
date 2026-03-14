package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mbentley/discord-webhook-queue/internal/alert"
	"github.com/mbentley/discord-webhook-queue/internal/config"
	"github.com/mbentley/discord-webhook-queue/internal/delivery"
	"github.com/mbentley/discord-webhook-queue/internal/store"
)

// Server is the HTTP server exposing the ingest, status, and metrics endpoints.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	engine  *delivery.Engine
	alerter *alert.Alerter
	http    *http.Server
}

// New creates and configures the HTTP server. Call ListenAndServe to start it.
func New(cfg *config.Config, s *store.Store, e *delivery.Engine, a *alert.Alerter) *Server {
	srv := &Server{cfg: cfg, store: s, engine: e, alerter: a}

	mux := http.NewServeMux()

	// Root info page — no auth, reveals nothing sensitive.
	mux.HandleFunc("GET /", srv.handleRoot)

	// Ingest endpoint is NEVER auth-gated: senders (discord.sh, Grafana, etc.)
	// cannot inject custom headers, so requiring a token here would break them.
	mux.HandleFunc("POST /webhooks/{id}/{token}", srv.handleIngest)

	// Status, metrics, and alert-test endpoints honour the optional auth token.
	if cfg.AuthToken != "" {
		mux.Handle("GET /metrics", srv.authMiddleware(promhttp.Handler()))
		mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
			srv.authMiddleware(http.HandlerFunc(srv.handleStatus)).ServeHTTP(w, r)
		})
		mux.HandleFunc("POST /alert/test", func(w http.ResponseWriter, r *http.Request) {
			srv.authMiddleware(http.HandlerFunc(srv.handleAlertTest)).ServeHTTP(w, r)
		})
	} else {
		mux.Handle("GET /metrics", promhttp.Handler())
		mux.HandleFunc("GET /status", srv.handleStatus)
		mux.HandleFunc("POST /alert/test", srv.handleAlertTest)
	}

	srv.http = &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: loggingMiddleware(mux),
		// Generous read timeout to accommodate large multipart uploads (e.g. Grafana images).
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return srv
}

// ListenAndServe starts the HTTP server. It returns http.ErrServerClosed on graceful shutdown.
// It uses a custom listener that strips non-standard Expect headers from incoming requests
// before Go's HTTP server processes them (e.g. discord.sh sends "Expect: application/json"
// which would otherwise cause Go to return 417 before the handler is invoked).
func (s *Server) ListenAndServe() error {
	l, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return err
	}
	slog.Info("HTTP server listening", "addr", s.cfg.ListenAddr)
	return s.http.Serve(&expectStrippingListener{l})
}

// expectStrippingListener wraps a net.Listener so that each accepted connection
// has non-standard Expect headers removed before Go's HTTP parser sees them.
type expectStrippingListener struct{ net.Listener }

func (l *expectStrippingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go stripExpectHeader(conn, pw)
	return &pipeConn{Conn: conn, r: pr}, nil
}

// stripExpectHeader reads the HTTP request headers from src, drops any Expect
// header whose value is not "100-continue", then pipes the rest through to dst.
func stripExpectHeader(src net.Conn, dst *io.PipeWriter) {
	br := bufio.NewReader(src)
	var hdr bytes.Buffer
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			dst.CloseWithError(err)
			return
		}
		// Blank line = end of headers.
		if line == "\r\n" || line == "\n" {
			hdr.WriteString(line)
			break
		}
		// Drop non-standard Expect values — only "100-continue" is valid.
		if strings.HasPrefix(strings.ToLower(line), "expect:") {
			val := strings.TrimSpace(line[len("expect:"):])
			if strings.EqualFold(val, "100-continue") {
				hdr.WriteString(line)
			}
			continue
		}
		hdr.WriteString(line)
	}
	if _, err := dst.Write(hdr.Bytes()); err != nil {
		dst.CloseWithError(err)
		return
	}
	io.Copy(dst, br)
	dst.Close()
}

// pipeConn overlays a net.Conn with a separate reader. HTTP responses are written
// directly to the underlying Conn; HTTP requests are read from the pre-processed pipe.
type pipeConn struct {
	net.Conn
	r io.Reader
}

func (c *pipeConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// responseRecorder wraps http.ResponseWriter to capture the status code.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

// loggingMiddleware logs each HTTP request (before) and response (after) with
// method, path, status, duration, and remote addr.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
		)
		start := time.Now()
		rr := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rr, r)
		slog.Info("http response",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rr.status,
			"duration", time.Since(start),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// authMiddleware rejects requests that do not carry the configured static token.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(s.cfg.AuthHeader) != s.cfg.AuthToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleIngest accepts a Discord webhook payload, stores it in the queue, and
// returns 204 No Content — matching Discord's own webhook response behaviour.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	webhookID := r.PathValue("id")
	webhookToken := r.PathValue("token")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		slog.Error("failed to read ingest request body", "err", err)
		http.Error(w, `{"error":"failed to read body"}`, http.StatusInternalServerError)
		return
	}

	// Preserve the full Content-Type header — multipart payloads embed the
	// boundary in it, which is required to replay the request to Discord.
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}

	id, err := s.store.Enqueue(webhookID, webhookToken, contentType, body)
	if err != nil {
		slog.Error("failed to enqueue message", "err", err)
		http.Error(w, `{"error":"failed to enqueue"}`, http.StatusInternalServerError)
		return
	}

	slog.Info("message enqueued",
		"id", id,
		"webhook_id", webhookID,
		"content_type", contentType,
		"bytes", len(body),
	)
	w.WriteHeader(http.StatusNoContent)
}

// statusResponse is the JSON shape returned by GET /status.
type statusResponse struct {
	State         string     `json:"state"`
	QueueDepth    int        `json:"queue_depth"`
	LastFailureAt *time.Time `json:"last_failure_at"`
}

// handleRoot returns a plain-text info page listing available endpoints.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "discord-webhook-queue\n\n")
	fmt.Fprintf(w, "Endpoints:\n")
	fmt.Fprintf(w, "  POST /webhooks/{id}/{token}   Enqueue a Discord webhook message\n")
	fmt.Fprintf(w, "  GET  /status                  Queue state and depth (JSON)\n")
	fmt.Fprintf(w, "  GET  /metrics                 Prometheus metrics\n")
	fmt.Fprintf(w, "  POST /alert/test              Send a test alert email (requires SMTP config)\n")
}

// handleAlertTest sends a test alert email and returns the result as JSON.
func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := s.alerter.SendTest(); err != nil {
		if err.Error() == "SMTP not configured" {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"error":"SMTP not configured"}`)
			return
		}
		slog.Error("alert test failed", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	slog.Info("test alert email sent")
	fmt.Fprintf(w, `{"ok":true}`)
}

// handleStatus returns the current daemon state as JSON. Always 200.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	engineStatus := s.engine.Status()

	depth, err := s.store.QueueDepth()
	if err != nil {
		slog.Error("failed to get queue depth for status", "err", err)
		depth = -1
	}

	resp := statusResponse{
		State:         engineStatus.State,
		QueueDepth:    depth,
		LastFailureAt: engineStatus.LastFailureAt,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

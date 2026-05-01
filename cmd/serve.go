package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/murdinc/lcdata/internal/lcdata"
	"github.com/spf13/cobra"
)

var servePort int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP + WebSocket server",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := lcdata.LoadConfig()
		if err != nil {
			return err
		}
		if servePort > 0 {
			cfg.Port = servePort
		}

		logger := lcdata.NewLogger(cfg.LogLevel)

		nodes, err := lcdata.LoadNodes(cfg.NodesPath)
		if err != nil {
			return err
		}
		logger.Info("nodes loaded", "count", len(nodes), "path", cfg.NodesPath)

		envCfg, err := lcdata.LoadEnvironmentConfigs()
		if err != nil {
			return err
		}

		runner := lcdata.NewRunner(nodes, envCfg, cfg, logger)

		store, err := lcdata.OpenStore(cfg.StorePath)
		if err != nil {
			logger.Error("failed to open store", "path", cfg.StorePath, "error", err)
			return err
		}
		defer store.Close()
		runner.SetStore(store)
		logger.Info("store opened", "path", cfg.StorePath)

		// Start hot-reload watcher for the nodes directory
		watchCtx, watchCancel := context.WithCancel(context.Background())
		defer watchCancel()
		if err := lcdata.WatchNodes(watchCtx, cfg.NodesPath, runner, logger); err != nil {
			logger.Error("failed to start node watcher", "error", err)
			// non-fatal: continue without hot reload
		}

		srv := newServer(cfg, runner, logger)

		server := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.Port),
			Handler: srv,
		}

		// Graceful shutdown
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

		go func() {
			logger.Info("lcdata serving", "port", cfg.Port)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("server error", "error", err)
				os.Exit(1)
			}
		}()

		<-stop
		logger.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(ctx)
	},
}

func init() {
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 0, "Override port from config")
}

// --- server setup ---

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func newServer(cfg *lcdata.Config, runner *lcdata.Runner, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS", "PUT"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
	}))

	if cfg.RequireJWT {
		r.Use(jwtMiddleware(cfg.JWTSecret))
	}
	if cfg.RateLimitRPS > 0 {
		r.Use(rateLimitMiddleware(cfg.RateLimitRPS, cfg.RateLimitBurst))
	}

	// Discovery
	r.Get("/api/health", handleHealth(cfg))
	r.Get("/api/info", handleInfo(cfg, runner))
	r.Get("/api/nodes", handleListNodes(runner))
	r.Get("/api/nodes/{name}", handleGetNode(runner))

	// Execution
	r.Post("/api/nodes/{name}/run", handleRun(cfg, runner))
	r.Post("/api/nodes/{name}/stream", handleStream(cfg, runner))
	r.Post("/api/nodes/{name}/audio", handleAudioRun(cfg, runner))
	r.Post("/api/nodes/{name}/audio/stream", handleAudioStream(cfg, runner))
	r.Get("/ws/nodes/{name}", handleWebSocket(cfg, runner, logger))

	// Run management
	r.Get("/api/runs", handleListRuns(runner))
	r.Get("/api/runs/{id}", handleGetRun(runner))
	r.Post("/api/runs/{id}/cancel", handleCancelRun(runner))

	return r
}

// --- middleware ---

type contextKey string

const claimsKey contextKey = "jwt_claims"

func jwtMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip health check
			if r.URL.Path == "/api/health" {
				next.ServeHTTP(w, r)
				return
			}

			tokenStr := ""
			if auth := r.Header.Get("Authorization"); len(auth) > 7 && auth[:7] == "Bearer " {
				tokenStr = auth[7:]
			} else if q := r.URL.Query().Get("token"); q != "" {
				tokenStr = q
			}

			if tokenStr == "" {
				writeError(w, http.StatusUnauthorized, "missing authorization token")
				return
			}

			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(secret), nil
			})
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid token: "+err.Error())
				return
			}

			// Store claims in context for downstream use (node scope check)
			ctx := context.WithValue(r.Context(), claimsKey, token.Claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// nodeAllowed checks whether the JWT claims permit access to the named node.
// If allowed_nodes is absent or empty, all nodes are allowed.
func nodeAllowed(r *http.Request, nodeName string) bool {
	claims, ok := r.Context().Value(claimsKey).(jwt.MapClaims)
	if !ok {
		return true // no JWT auth active
	}
	allowedRaw, ok := claims["allowed_nodes"]
	if !ok {
		return true // no restriction in token
	}
	allowed, ok := allowedRaw.([]any)
	if !ok || len(allowed) == 0 {
		return true
	}
	for _, v := range allowed {
		if s, ok := v.(string); ok && s == nodeName {
			return true
		}
	}
	return false
}

// --- rate limiter ---

// tokenBucket is a simple per-key token bucket.
type tokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rps     float64
	burst   int
}

type bucket struct {
	tokens   float64
	lastFill time.Time
}

func newTokenBucket(rps, burst int) *tokenBucket {
	return &tokenBucket{
		buckets: make(map[string]*bucket),
		rps:     float64(rps),
		burst:   burst,
	}
}

func (tb *tokenBucket) allow(key string) (bool, time.Duration) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(tb.burst), lastFill: now}
		tb.buckets[key] = b
	}

	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens = min(float64(tb.burst), b.tokens+elapsed*tb.rps)
	b.lastFill = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	retryAfter := time.Duration((1-b.tokens)/tb.rps*1000) * time.Millisecond
	return false, retryAfter
}

func rateLimitMiddleware(rps, burst int) func(http.Handler) http.Handler {
	if burst <= 0 {
		burst = rps * 2
	}
	limiter := newTokenBucket(rps, burst)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Key by JWT sub claim, fall back to IP
			key := r.RemoteAddr
			if claims, ok := r.Context().Value(claimsKey).(jwt.MapClaims); ok {
				if sub, ok := claims["sub"].(string); ok && sub != "" {
					key = sub
				}
			}

			allowed, retryAfter := limiter.allow(key)
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds()+1)))
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- handlers ---

func handleHealth(cfg *lcdata.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"version": Version,
		})
	}
}

func handleInfo(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes := runner.Nodes()
		nodeTypes := []string{"llm", "stt", "tts", "command", "database", "http", "transform", "pipeline"}
		writeJSON(w, http.StatusOK, map[string]any{
			"version":     Version,
			"node_count":  len(nodes),
			"node_types":  nodeTypes,
			"nodes_path":  cfg.NodesPath,
			"require_jwt": cfg.RequireJWT,
		})
	}
}

func handleListNodes(runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		nodes := runner.Nodes()
		summaries := make([]map[string]any, len(nodes))
		for i, n := range nodes {
			summaries[i] = n.Summary()
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": summaries,
			"count": len(nodes),
		})
	}
}

func handleGetNode(runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		node, err := runner.Nodes().Get(name)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, node)
	}
}

func handleRun(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !nodeAllowed(r, name) {
			writeError(w, http.StatusForbidden, "token does not permit access to node: "+name)
			return
		}

		var req lcdata.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), cfg.RunTimeoutDuration)
		defer cancel()

		run, err := runner.RunSync(ctx, req, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		status := http.StatusOK
		if run.Status == lcdata.RunStatusFailed {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, run)
	}
}

// handleAudioRun accepts a multipart/form-data upload with an "audio" file field,
// saves it to a temp file, and runs the named node with audio_url set to the local path.
// This lets clients POST raw audio directly rather than hosting it over HTTP.
func handleAudioRun(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !nodeAllowed(r, name) {
			writeError(w, http.StatusForbidden, "token does not permit access to node: "+name)
			return
		}

		if err := r.ParseMultipartForm(50 << 20); err != nil { // 50 MB max
			writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
			return
		}

		file, header, err := r.FormFile("audio")
		if err != nil {
			writeError(w, http.StatusBadRequest, "audio field is required")
			return
		}
		defer file.Close()

		// Determine extension from content-type or original filename
		ext := filepath.Ext(header.Filename)
		if ext == "" {
			ct := header.Header.Get("Content-Type")
			ext = audioExtFromMIME(ct)
		}

		tmpFile, err := os.CreateTemp("", "lcdata-audio-*"+ext)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create temp file: "+err.Error())
			return
		}
		defer os.Remove(tmpFile.Name())

		if _, err := io.Copy(tmpFile, file); err != nil {
			tmpFile.Close()
			writeError(w, http.StatusInternalServerError, "failed to write audio: "+err.Error())
			return
		}
		tmpFile.Close()

		envName := r.FormValue("env")
		if envName == "" {
			envName = "default"
		}

		runInput := map[string]any{
			"audio_url": tmpFile.Name(),
		}
		if histJSON := r.FormValue("history"); histJSON != "" {
			var history []any
			if err := json.Unmarshal([]byte(histJSON), &history); err == nil && len(history) > 0 {
				runInput["history"] = history
			}
		}

		req := lcdata.RunRequest{
			Input: runInput,
			Env:   envName,
		}

		ctx, cancel := context.WithTimeout(r.Context(), cfg.RunTimeoutDuration)
		defer cancel()

		run, err := runner.RunSync(ctx, req, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		status := http.StatusOK
		if run.Status == lcdata.RunStatusFailed {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, run)
	}
}

// handleAudioStream is like handleAudioRun but streams events as NDJSON
// (one JSON object per line) instead of waiting for the run to finish.
// Clients can read line-by-line and display step progress in real time.
func handleAudioStream(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !nodeAllowed(r, name) {
			writeError(w, http.StatusForbidden, "token does not permit access to node: "+name)
			return
		}

		if err := r.ParseMultipartForm(50 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "failed to parse multipart form: "+err.Error())
			return
		}

		file, header, err := r.FormFile("audio")
		if err != nil {
			writeError(w, http.StatusBadRequest, "audio field is required")
			return
		}
		defer file.Close()

		ext := filepath.Ext(header.Filename)
		if ext == "" {
			ct := header.Header.Get("Content-Type")
			ext = audioExtFromMIME(ct)
		}

		tmpFile, err := os.CreateTemp("", "lcdata-audio-*"+ext)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create temp file: "+err.Error())
			return
		}
		defer os.Remove(tmpFile.Name())

		if _, err := io.Copy(tmpFile, file); err != nil {
			tmpFile.Close()
			writeError(w, http.StatusInternalServerError, "failed to write audio: "+err.Error())
			return
		}
		tmpFile.Close()

		envName := r.FormValue("env")
		if envName == "" {
			envName = "default"
		}

		runInput := map[string]any{
			"audio_url": tmpFile.Name(),
		}
		if histJSON := r.FormValue("history"); histJSON != "" {
			var history []any
			if err := json.Unmarshal([]byte(histJSON), &history); err == nil && len(history) > 0 {
				runInput["history"] = history
			}
		}

		req := lcdata.RunRequest{
			Input: runInput,
			Env:   envName,
		}

		run, err := runner.Start(r.Context(), req, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, canFlush := w.(http.Flusher)

		for event := range run.Events {
			line := append(event.JSON(), '\n')
			if _, werr := w.Write(line); werr != nil {
				runner.CancelRun(run.ID)
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

func handleStream(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !nodeAllowed(r, name) {
			writeError(w, http.StatusForbidden, "token does not permit access to node: "+name)
			return
		}

		var req lcdata.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		run, err := runner.Start(r.Context(), req, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Server-Sent Events
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}

		for event := range run.Events {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

func handleWebSocket(cfg *lcdata.Config, runner *lcdata.Runner, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		if !nodeAllowed(r, name) {
			writeError(w, http.StatusForbidden, "token does not permit access to node: "+name)
			return
		}

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("websocket upgrade error", "error", err)
			return
		}
		defer conn.Close()

		// Read the run request from the first WS message
		var req lcdata.RunRequest
		if err := conn.ReadJSON(&req); err != nil {
			logger.Error("websocket read error", "error", err)
			return
		}

		run, err := runner.Start(r.Context(), req, name)
		if err != nil {
			conn.WriteJSON(lcdata.Event{
				Event: lcdata.EventRunFailed,
				Error: err.Error(),
			})
			return
		}

		for event := range run.Events {
			if err := conn.WriteJSON(event); err != nil {
				logger.Error("websocket write error", "run_id", run.ID, "error", err)
				runner.CancelRun(run.ID)
				return
			}
		}
	}
}

func handleListRuns(runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runs := runner.ListRuns()
		writeJSON(w, http.StatusOK, map[string]any{
			"runs":  runs,
			"count": len(runs),
		})
	}
}

func handleGetRun(runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		run, err := runner.GetRun(id)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, run)
	}
}

func handleCancelRun(runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := runner.CancelRun(id); err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": id})
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func audioExtFromMIME(ct string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "audio/wav"), strings.Contains(ct, "audio/wave"):
		return ".wav"
	case strings.Contains(ct, "audio/mpeg"), strings.Contains(ct, "audio/mp3"):
		return ".mp3"
	case strings.Contains(ct, "audio/ogg"):
		return ".ogg"
	case strings.Contains(ct, "audio/flac"):
		return ".flac"
	case strings.Contains(ct, "audio/mp4"), strings.Contains(ct, "audio/m4a"):
		return ".m4a"
	case strings.Contains(ct, "audio/webm"):
		return ".webm"
	default:
		return ".wav"
	}
}

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
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

		nodes, err := lcdata.LoadNodes(cfg.NodesPath)
		if err != nil {
			return err
		}
		log.Printf("Loaded %d nodes from %s", len(nodes), cfg.NodesPath)

		envCfg, err := lcdata.LoadEnvironmentConfigs()
		if err != nil {
			return err
		}

		runner := lcdata.NewRunner(nodes, envCfg, cfg)
		srv := newServer(cfg, nodes, runner)

		server := &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.Port),
			Handler: srv,
		}

		// Graceful shutdown
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

		go func() {
			log.Printf("lcdata serving on :%d", cfg.Port)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("server error: %v", err)
			}
		}()

		<-stop
		log.Println("Shutting down...")
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

func newServer(cfg *lcdata.Config, nodes lcdata.Nodes, runner *lcdata.Runner) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
	}))

	if cfg.RequireJWT {
		r.Use(jwtMiddleware(cfg.JWTSecret))
	}

	// Discovery
	r.Get("/api/health", handleHealth(cfg))
	r.Get("/api/info", handleInfo(cfg, nodes))
	r.Get("/api/nodes", handleListNodes(nodes))
	r.Get("/api/nodes/{name}", handleGetNode(nodes))

	// Execution
	r.Post("/api/nodes/{name}/run", handleRun(cfg, runner))
	r.Post("/api/nodes/{name}/stream", handleStream(cfg, runner))
	r.Get("/ws/nodes/{name}", handleWebSocket(cfg, runner))

	// Run management
	r.Get("/api/runs", handleListRuns(runner))
	r.Get("/api/runs/{id}", handleGetRun(runner))
	r.Post("/api/runs/{id}/cancel", handleCancelRun(runner))

	return r
}

// --- middleware ---

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

			_, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return []byte(secret), nil
			})
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid token: "+err.Error())
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

func handleInfo(cfg *lcdata.Config, nodes lcdata.Nodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func handleListNodes(nodes lcdata.Nodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func handleGetNode(nodes lcdata.Nodes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		node, err := nodes.Get(name)
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

func handleStream(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")

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

func handleWebSocket(cfg *lcdata.Config, runner *lcdata.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("websocket upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Read the run request from the first WS message
		var req lcdata.RunRequest
		if err := conn.ReadJSON(&req); err != nil {
			log.Printf("websocket read error: %v", err)
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
				log.Printf("websocket write error: %v", err)
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

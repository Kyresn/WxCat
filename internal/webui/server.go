// Package webui serves the small embedded admin panel.
package webui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/logging"
	"wxcat/internal/media"
	"wxcat/internal/onebot"
)

//go:embed static
var staticFS embed.FS

// Server is the admin panel.
type Server struct {
	cfg     *config.Config
	cfgPath string
	be      backend.Backend
	ids     *onebot.Resolver
	hub     *onebot.Hub
	actions *onebot.Actions
	media   *media.Store
	log     *slog.Logger
	ring    *logging.Ring

	srv *http.Server
}

// New creates the admin panel.
func New(cfg *config.Config, cfgPath string, be backend.Backend, ids *onebot.Resolver, hub *onebot.Hub, actions *onebot.Actions, store *media.Store, log *slog.Logger, ring *logging.Ring) *Server {
	return &Server{cfg: cfg, cfgPath: cfgPath, be: be, ids: ids, hub: hub, actions: actions, media: store, log: log, ring: ring}
}

// Start begins serving.
func (s *Server) Start(ctx context.Context) error {
	addr := net.JoinHostPort(s.cfg.WebUI.Host, strconv.Itoa(s.cfg.WebUI.Port))
	s.srv = &http.Server{Addr: addr, Handler: s.Handler()}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("webui listen %s: %w", addr, err)
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("webui stopped", "err", err)
		}
	}()
	s.log.Info("webui listening", "addr", addr)
	return nil
}

// Stop shuts the panel down.
func (s *Server) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// Handler returns the panel mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Unauthenticated liveness probe for container health checks.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/api/status", s.withAuth(s.handleStatus))
	mux.HandleFunc("/api/config", s.withAuth(s.handleConfig))
	mux.HandleFunc("/api/send", s.withAuth(s.handleSend))
	mux.HandleFunc("/api/logs", s.withAuth(s.handleLogs))
	mux.HandleFunc("/api/qr", s.withAuth(s.handleQR))
	if s.media != nil {
		mux.Handle("/media/", s.media)
	}

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		s.log.Error("embedded static assets missing", "err", err)
		return mux
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.WebUI.Token != "" && !s.authorized(r) {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next(w, r)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	parts := splitToken(auth)
	if parts == s.cfg.WebUI.Token {
		return true
	}
	return r.URL.Query().Get("token") == s.cfg.WebUI.Token
}

func splitToken(h string) string {
	for _, prefix := range []string{"Bearer ", "Token "} {
		if len(h) > len(prefix) && h[:len(prefix)] == prefix {
			return h[len(prefix):]
		}
	}
	return ""
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	health, err := s.be.Health(r.Context())
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	self, selfErr := s.be.Self(r.Context())
	payload := map[string]any{
		"app":         "wxcat",
		"version":     onebot.Version,
		"backend":     s.be.Name(),
		"online":      health.Online,
		"detail":      detail,
		"since":       health.Since,
		"connections": s.hub.Count(),
		"id_mode":     s.cfg.IDMode,
		"self": map[string]any{
			"wxid":     self.ID,
			"nickname": self.Name,
			"self_id":  s.ids.Self(),
			"error":    errString(selfErr),
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.cfg)
	case http.MethodPut, http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var incoming config.Config
		if err := json.Unmarshal(body, &incoming); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid config: "+err.Error())
			return
		}
		incoming.Normalize()
		*s.cfg = incoming
		if err := s.cfg.Save(s.cfgPath); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"saved":   true,
			"message": "configuration saved; restart the container to apply listener changes",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Kind string `json:"kind"`
		ID   any    `json:"id"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Kind == "" {
		req.Kind = "private"
	}
	params := map[string]any{
		"message": []onebot.Segment{onebot.Text(req.Text)},
	}
	action := "send_private_msg"
	if req.Kind == "group" {
		action = "send_group_msg"
		params["group_id"] = req.ID
	} else {
		params["user_id"] = req.ID
	}
	body, _ := json.Marshal(params)
	data, aerr := s.actions.Handle(r.Context(), action, body)
	if aerr != nil {
		writeErr(w, http.StatusOK, aerr.Message)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": data})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if v := r.URL.Query().Get("tail"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": s.ring.Tail(n)})
}

// handleQR captures the virtual desktop so the user can scan the login QR
// without opening noVNC.
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":1"
	}
	cmd := exec.CommandContext(ctx, "import", "-display", display, "-window", "root", "png:-")
	out, err := cmd.Output()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable,
			"cannot capture the desktop; open noVNC instead ("+err.Error()+")")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

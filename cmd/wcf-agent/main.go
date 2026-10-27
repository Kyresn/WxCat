// Command wcf-agent runs next to WeChat inside the all-in-one image and exposes
// the two media operations a remote WxCat gateway cannot perform itself:
// decrypting files on the WeChat host and staging files for sending.
//
// It is only needed when the gateway and WeChat run on different machines, for
// example an arm64 gateway with an x86_64 WeChat host.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wxcat/internal/wcfpath"
	wcf "wxcat/third_party/wcf"
)

const maxUpload = 64 << 20

type agent struct {
	cmdAddr   string
	msgAddr   string
	uploadDir string
	token     string
	log       *slog.Logger

	mu     sync.Mutex
	client *wcf.Client
	seq    atomic.Uint64
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	a := &agent{
		cmdAddr:   envOr("WCF_CMD_ADDR", "tcp://127.0.0.1:10086"),
		msgAddr:   envOr("WCF_MSG_ADDR", "tcp://127.0.0.1:10087"),
		uploadDir: envOr("WCF_AGENT_UPLOAD_DIR", "/app/data/upload"),
		token:     os.Getenv("WCF_AGENT_TOKEN"),
		log:       logger,
	}
	if err := os.MkdirAll(a.uploadDir, 0o755); err != nil {
		logger.Error("cannot create upload dir", "err", err)
		os.Exit(1)
	}

	addr := envOr("WCF_AGENT_ADDR", ":11090")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/decrypt", a.withAuth(a.handleDecrypt))
	mux.HandleFunc("/upload", a.withAuth(a.handleUpload))

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Info("wcf-agent listening", "addr", addr, "upload_dir", a.uploadDir, "auth", a.token != "")
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("wcf-agent stopped", "err", err)
		os.Exit(1)
	}
}

func (a *agent) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.token != "" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != a.token {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (a *agent) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Do not touch WeChatFerry here: the RPC transport is point-to-point, and a
	// health probe must not open an extra client connection.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *agent) handleDecrypt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Src string `json:"src"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.Src == "" {
		http.Error(w, "src is required", http.StatusBadRequest)
		return
	}

	client, err := a.getClient()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	dst, err := os.CreateTemp("", "wxcat-dec-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dstPath := dst.Name()
	_ = dst.Close()
	defer os.Remove(dstPath)

	if _, err := client.DecryptImage(req.Src, wcfpath.ToWine(dstPath)); err != nil {
		a.log.Debug("DecryptImage failed, falling back to the raw file", "src", req.Src, "err", err)
	}

	data, err := os.ReadFile(dstPath)
	if err != nil || len(data) == 0 {
		data, err = os.ReadFile(linuxPath(req.Src))
	}
	if err != nil {
		http.Error(w, "cannot read media: "+err.Error(), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (a *agent) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := sanitize(r.URL.Query().Get("name"))
	if name == "" {
		name = "file.bin"
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxUpload))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(data) == 0 {
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	filename := fmt.Sprintf("%d_%d_%s", time.Now().UnixNano(), a.seq.Add(1), name)
	full := filepath.Join(a.uploadDir, filename)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.log.Info("staged upload", "file", full, "bytes", len(data))
	writeJSON(w, http.StatusOK, map[string]any{"path": wcfpath.ToWine(full)})
}

func (a *agent) getClient() (*wcf.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		return a.client, nil
	}
	client, err := wcf.New(a.cmdAddr, a.msgAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to wechatferry: %w", err)
	}
	a.client = client
	return client, nil
}

func (a *agent) resetClient() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client != nil {
		_ = a.client.Close()
		a.client = nil
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// linuxPath maps a Windows path inside the Wine prefix to the Linux path the
// agent can read directly. Used only as a fallback when DecryptImage fails.
func linuxPath(win string) string {
	if len(win) < 3 || win[1] != ':' {
		return win
	}
	drive := strings.ToLower(win[:1])
	rest := strings.ReplaceAll(win[3:], `\`, "/")
	if drive == "z" {
		return "/" + strings.TrimPrefix(rest, "/")
	}
	prefix := os.Getenv("WINEPREFIX")
	if prefix == "" {
		prefix = "/home/wechat/.wine"
	}
	return filepath.Join(prefix, "drive_"+drive, rest)
}

func sanitize(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, name)
	if name == "." || name == ".." {
		return ""
	}
	return name
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

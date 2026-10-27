package onebot

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"wxcat/internal/media"
)

// HTTPAPI exposes OneBot actions over HTTP.
type HTTPAPI struct {
	actions *Actions
	media   *media.Store
	token   string
	debug   bool
	log     *slog.Logger
}

// NewHTTPAPI builds an HTTP action handler.
func NewHTTPAPI(actions *Actions, store *media.Store, token string, debug bool, log *slog.Logger) *HTTPAPI {
	return &HTTPAPI{actions: actions, media: store, token: token, debug: debug, log: log}
}

// Handler returns the mux for this endpoint. It serves /<action> and a small
// /health probe.
func (h *HTTPAPI) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	if h.media != nil {
		mux.Handle("/media/", h.media)
	}
	mux.HandleFunc("/", h.handle)
	return mux
}

func (h *HTTPAPI) handle(w http.ResponseWriter, r *http.Request) {
	action := strings.Trim(r.URL.Path, "/")
	if action == "" {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "app": "wxcat"})
		return
	}
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, ActionResponse{
			Status: "failed", Retcode: RetNoPermission, Message: "invalid access token",
		})
		return
	}

	var body []byte
	switch r.Method {
	case http.MethodPost:
		b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ActionResponse{Status: "failed", Retcode: RetBadRequest, Message: err.Error()})
			return
		}
		body = b
	case http.MethodGet:
		body = h.queryToParams(r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if len(body) == 0 {
		body = []byte("{}")
	}

	if h.debug {
		h.log.Debug("http action", "action", action, "params", string(body))
	}

	data, aerr := h.actions.Handle(r.Context(), action, body)
	if aerr != nil {
		writeJSON(w, http.StatusOK, ActionResponse{
			Status:  "failed",
			Retcode: aerr.Retcode,
			Message: aerr.Message,
			Wording: aerr.Message,
			Data:    nil,
		})
		return
	}
	writeJSON(w, http.StatusOK, ActionResponse{Status: "ok", Retcode: 0, Data: data})
}

func (h *HTTPAPI) authorized(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if tokenMatches(auth, h.token) {
		return true
	}
	return r.URL.Query().Get("access_token") == h.token
}

func tokenMatches(header, token string) bool {
	if header == "" {
		return false
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return false
	}
	switch strings.ToLower(parts[0]) {
	case "bearer", "token":
		return parts[1] == token
	}
	return false
}

func (h *HTTPAPI) queryToParams(r *http.Request) []byte {
	q := r.URL.Query()
	out := make(map[string]any, len(q))
	for k, v := range q {
		if k == "access_token" || len(v) == 0 {
			continue
		}
		out[k] = v[0]
	}
	b, _ := json.Marshal(out)
	return b
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

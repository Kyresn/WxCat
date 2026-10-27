// Package media caches decrypted WeChat attachments and serves them over HTTP
// so OneBot clients can fetch images by URL instead of base64.
package media

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store writes media into a directory and exposes it at /media/<name>.
type Store struct {
	dir     string
	baseURL string

	mu   sync.RWMutex
	urls map[string]string // key -> relative file name
}

// New creates a store rooted at dir. baseURL may be empty, in which case
// callers fall back to data URLs.
func New(dir, baseURL string) (*Store, error) {
	if dir == "" {
		dir = "data/media"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create media dir: %w", err)
	}
	return &Store{
		dir:     dir,
		baseURL: strings.TrimRight(baseURL, "/"),
		urls:    make(map[string]string),
	}, nil
}

// BaseURL reports the configured public prefix.
func (s *Store) BaseURL() string { return s.baseURL }

// Put stores data under a deterministic name derived from key and returns the
// public URL. When baseURL is empty the returned URL is relative.
func (s *Store) Put(key string, data []byte, name string) (string, error) {
	ext := filepath.Ext(name)
	if ext == "" {
		ext = extFromData(data)
	}
	sum := sha1.Sum([]byte(key))
	file := hex.EncodeToString(sum[:8]) + ext

	full := filepath.Join(s.dir, file)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", fmt.Errorf("write media: %w", err)
	}

	s.mu.Lock()
	s.urls[key] = file
	s.mu.Unlock()

	return s.URLFor(file), nil
}

// URLFor returns the public URL of a stored file name.
func (s *Store) URLFor(file string) string {
	if s.baseURL == "" {
		return "/media/" + file
	}
	return s.baseURL + "/media/" + file
}

// Path resolves a stored key to an absolute path.
func (s *Store) Path(key string) (string, bool) {
	s.mu.RLock()
	file, ok := s.urls[key]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	return filepath.Join(s.dir, file), true
}

// Dir exposes the backing directory.
func (s *Store) Dir() string { return s.dir }

// ServeHTTP serves /media/<file>.
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/media/")
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.dir, name)
	if _, err := os.Stat(full); err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, full)
}

// DataURL encodes bytes as a data URL so clients that cannot reach the media
// endpoint still receive the payload.
func DataURL(data []byte, name string) string {
	mimeType := mime.TypeByExtension(filepath.Ext(name))
	if mimeType == "" {
		mimeType = mimeFromData(data)
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func extFromData(data []byte) string {
	switch {
	case len(data) > 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		return ".png"
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		return ".jpg"
	case len(data) > 6 && string(data[0:4]) == "GIF8":
		return ".gif"
	case len(data) > 12 && string(data[8:12]) == "WEBP":
		return ".webp"
	default:
		return ".bin"
	}
}

func mimeFromData(data []byte) string {
	switch extFromData(data) {
	case ".png":
		return "image/png"
	case ".jpg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

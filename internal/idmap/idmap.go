// Package idmap translates WeChat string identifiers into the integer ids that
// OneBot 11 expects.
//
// WeChat uses strings such as "wxid_abc123" and "12345678@chatroom". Strict
// OneBot clients parse user_id/group_id as integers, so WxCat derives a stable
// 63-bit number from the string and persists any collision resolution. The
// mapping survives restarts, which matters because AstrBot and DSH store
// session ids that embed these numbers.
package idmap

import (
	"encoding/json"
	"fmt"
	"hash/crc64"
	"os"
	"path/filepath"
	"sync"
)

// Kind separates the user and group id spaces.
type Kind string

const (
	KindUser  Kind = "user"
	KindGroup Kind = "group"
)

// idMask keeps generated ids below 2^53 so JavaScript clients (and any client
// that parses ids as float64) do not lose precision.
const idMask = 1<<53 - 1

type entry struct {
	ID   int64  `json:"id"`
	Wxid string `json:"wxid"`
	Kind Kind   `json:"kind"`
}

type fileFormat struct {
	Entries []entry `json:"entries"`
}

// Store is a JSON-backed, restart-stable wxid <-> int64 mapping.
type Store struct {
	mu    sync.Mutex
	path  string
	byKey map[string]int64 // "kind|wxid" -> id
	byID  map[int64]entry
}

// Open loads the mapping file, creating an empty store when it is missing.
func Open(path string) (*Store, error) {
	s := &Store{
		path:  path,
		byKey: make(map[string]int64),
		byID:  make(map[int64]entry),
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read id map: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse id map %s: %w", path, err)
	}
	for _, e := range f.Entries {
		if e.Wxid == "" || e.ID == 0 {
			continue
		}
		s.byKey[key(e.Kind, e.Wxid)] = e.ID
		s.byID[e.ID] = e
	}
	return s, nil
}

func key(k Kind, wxid string) string { return string(k) + "|" + wxid }

// Num returns the stable numeric id for wxid, allocating one on first use.
func (s *Store) Num(kind Kind, wxid string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, ok := s.byKey[key(kind, wxid)]; ok {
		return id
	}

	id := derive(kind, wxid)
	for {
		if _, taken := s.byID[id]; !taken {
			break
		}
		id = (id + 1) & idMask
		if id == 0 {
			id = 1
		}
	}
	e := entry{ID: id, Wxid: wxid, Kind: kind}
	s.byKey[key(kind, wxid)] = id
	s.byID[id] = e
	_ = s.saveLocked()
	return id
}

// Lookup resolves a numeric id back to its wxid.
func (s *Store) Lookup(id int64) (string, Kind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byID[id]
	if !ok {
		return "", "", false
	}
	return e.Wxid, e.Kind, true
}

// Wxid resolves a numeric id to its wxid.
func (s *Store) Wxid(id int64) (string, bool) {
	wxid, _, ok := s.Lookup(id)
	return wxid, ok
}

func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	f := fileFormat{Entries: make([]entry, 0, len(s.byID))}
	for _, e := range s.byID {
		f.Entries = append(f.Entries, e)
	}
	data, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Save flushes the current mapping to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func derive(kind Kind, wxid string) int64 {
	h := crc64.Checksum([]byte(key(kind, wxid)), crc64.MakeTable(crc64.ECMA))
	id := int64(h & idMask)
	if id == 0 {
		id = 1
	}
	return id
}

package onebot

import (
	"fmt"
	"strconv"
	"strings"

	"wxcat/internal/idmap"
)

// Resolver converts between WeChat string ids and the ids exposed on the
// OneBot wire.
type Resolver struct {
	store  *idmap.Store
	raw    bool
	selfID string
}

// NewResolver builds a resolver. raw=true exposes wxid strings unchanged.
func NewResolver(store *idmap.Store, raw bool) *Resolver {
	return &Resolver{store: store, raw: raw}
}

// SetSelf records the logged-in wxid.
func (r *Resolver) SetSelf(wxid string) { r.selfID = wxid }

// SelfWxid returns the logged-in wxid.
func (r *Resolver) SelfWxid() string { return r.selfID }

// Raw reports whether ids are exposed as strings.
func (r *Resolver) Raw() bool { return r.raw }

// UserID renders a wxid as a OneBot user_id.
func (r *Resolver) UserID(wxid string) any {
	if r.raw || wxid == "" {
		return wxid
	}
	return r.store.Num(idmap.KindUser, wxid)
}

// GroupID renders a room id as a OneBot group_id.
func (r *Resolver) GroupID(roomID string) any {
	if r.raw || roomID == "" {
		return roomID
	}
	return r.store.Num(idmap.KindGroup, roomID)
}

// Self renders the bot's own user_id.
func (r *Resolver) Self() any { return r.UserID(r.selfID) }

// SelfNum always returns a numeric id, even in raw mode. It is used for the
// X-Self-ID header and for heartbeat payloads.
func (r *Resolver) SelfNum() int64 {
	if r.selfID == "" {
		return 0
	}
	return r.store.Num(idmap.KindUser, r.selfID)
}

// ResolveUser maps an incoming user_id back to a wxid.
func (r *Resolver) ResolveUser(v any) (string, error) {
	return r.resolve(idmap.KindUser, v)
}

// ResolveGroup maps an incoming group_id back to a room id.
func (r *Resolver) ResolveGroup(v any) (string, error) {
	return r.resolve(idmap.KindGroup, v)
}

func (r *Resolver) resolve(kind idmap.Kind, v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", fmt.Errorf("missing id")
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", fmt.Errorf("empty id")
		}
		if r.raw || looksLikeWxid(s) {
			return s, nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid id %q", s)
		}
		return r.byNumber(kind, n)
	case float64:
		return r.byNumber(kind, int64(t))
	case int64:
		return r.byNumber(kind, t)
	case int:
		return r.byNumber(kind, int64(t))
	case jsonNumber:
		n, err := t.Int64()
		if err != nil {
			return "", err
		}
		return r.byNumber(kind, n)
	default:
		return "", fmt.Errorf("unsupported id type %T", v)
	}
}

func (r *Resolver) byNumber(kind idmap.Kind, n int64) (string, error) {
	wxid, gotKind, ok := r.store.Lookup(n)
	if !ok {
		return "", fmt.Errorf("unknown %s id %d", kind, n)
	}
	if gotKind != kind {
		return "", fmt.Errorf("id %d is a %s id, not a %s id", n, gotKind, kind)
	}
	return wxid, nil
}

// looksLikeWxid lets raw wxid strings through even in numeric mode. This keeps
// clients that read ids from get_friend_list and echo them back working.
func looksLikeWxid(s string) bool {
	return strings.HasPrefix(s, "wxid_") || strings.HasSuffix(s, "@chatroom") || strings.HasPrefix(s, "gh_")
}

// jsonNumber mirrors encoding/json.Number without importing it at the call
// site; kept as a named type for the type switch above.
type jsonNumber interface{ Int64() (int64, error) }

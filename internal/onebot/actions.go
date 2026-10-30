package onebot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/media"
)

// Version is reported by get_version_info. It is overridden at build time via
// -ldflags "-X wxcat/internal/onebot.Version=<tag>".
var Version = "0.1.5"

const maxDownload = 64 << 20

// Actions routes OneBot actions to the WeChat backend.
type Actions struct {
	cfg    *config.Config
	be     backend.Backend
	ids    *Resolver
	media  *media.Store
	log    *slog.Logger
	cache  *MsgCache
	tmpDir string
	client *http.Client
}

// NewActions builds the action router.
func NewActions(cfg *config.Config, be backend.Backend, ids *Resolver, store *media.Store, log *slog.Logger) *Actions {
	tmp := filepath.Join(os.TempDir(), "wxcat")
	_ = os.MkdirAll(tmp, 0o755)
	return &Actions{
		cfg:    cfg,
		be:     be,
		ids:    ids,
		media:  store,
		log:    log,
		cache:  NewMsgCache(512),
		tmpDir: tmp,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Cache exposes the message cache for inbound events.
func (a *Actions) Cache() *MsgCache { return a.cache }

// Handle executes one action. A nil *ActionError means success.
func (a *Actions) Handle(ctx context.Context, action string, raw json.RawMessage) (any, *ActionError) {
	p := params{}
	if len(raw) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&p); err != nil {
			return nil, errBad("params must be a JSON object")
		}
	}

	switch action {
	case "send_private_msg":
		return a.send(ctx, backend.KindPrivate, p)
	case "send_group_msg":
		return a.send(ctx, backend.KindGroup, p)
	case "send_msg":
		return a.send(ctx, "", p)

	case "get_login_info":
		self, err := a.be.Self(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		return map[string]any{
			"user_id":  a.ids.UserID(self.ID),
			"nickname": self.Name,
		}, nil

	case "get_friend_list":
		contacts, err := a.be.Contacts(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		out := make([]map[string]any, 0, len(contacts))
		for _, c := range contacts {
			if c.IsGroup {
				continue
			}
			out = append(out, map[string]any{
				"user_id":  a.ids.UserID(c.ID),
				"nickname": c.Name,
				"remark":   c.Remark,
			})
		}
		return out, nil

	case "get_group_list":
		contacts, err := a.be.Contacts(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		out := make([]map[string]any, 0, len(contacts))
		for _, c := range contacts {
			if !c.IsGroup {
				continue
			}
			out = append(out, map[string]any{
				"group_id":         a.ids.GroupID(c.ID),
				"group_name":       c.Name,
				"member_count":     c.MemberCount,
				"max_member_count": 500,
			})
		}
		return out, nil

	case "get_group_info":
		gid, aerr := a.groupParam(p)
		if aerr != nil {
			return nil, aerr
		}
		contacts, err := a.be.Contacts(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, c := range contacts {
			if c.IsGroup && c.ID == gid {
				return map[string]any{
					"group_id":         a.ids.GroupID(c.ID),
					"group_name":       c.Name,
					"member_count":     c.MemberCount,
					"max_member_count": 500,
				}, nil
			}
		}
		return nil, errNotFound("group not found")

	case "get_group_member_list":
		gid, aerr := a.groupParam(p)
		if aerr != nil {
			return nil, aerr
		}
		members, err := a.be.GroupMembers(ctx, gid)
		if err != nil {
			return nil, mapErr(err)
		}
		out := make([]map[string]any, 0, len(members))
		for _, m := range members {
			out = append(out, map[string]any{
				"group_id": a.ids.GroupID(gid),
				"user_id":  a.ids.UserID(m.ID),
				"nickname": m.Name,
				"card":     m.Card,
				"role":     m.Role,
			})
		}
		return out, nil

	case "get_group_member_info":
		gid, aerr := a.groupParam(p)
		if aerr != nil {
			return nil, aerr
		}
		uid, aerr := a.userParam(p)
		if aerr != nil {
			return nil, aerr
		}
		members, err := a.be.GroupMembers(ctx, gid)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, m := range members {
			if m.ID == uid {
				return map[string]any{
					"group_id": a.ids.GroupID(gid),
					"user_id":  a.ids.UserID(m.ID),
					"nickname": m.Name,
					"card":     m.Card,
					"role":     m.Role,
				}, nil
			}
		}
		return nil, errNotFound("member not found")

	case "get_stranger_info":
		uid, aerr := a.userParam(p)
		if aerr != nil {
			return nil, aerr
		}
		contacts, err := a.be.Contacts(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, c := range contacts {
			if c.ID == uid {
				return map[string]any{
					"user_id":  a.ids.UserID(c.ID),
					"nickname": c.Name,
					"remark":   c.Remark,
					"sex":      "unknown",
					"age":      0,
				}, nil
			}
		}
		return nil, errNotFound("user not found")

	case "get_msg":
		id, ok := p.int64("message_id")
		if !ok {
			return nil, errBad("message_id is required")
		}
		rec, ok := a.cache.Get(id)
		if !ok {
			return nil, errNotFound("message not found")
		}
		return a.renderRecord(rec), nil

	case "delete_msg":
		id, ok := p.int64("message_id")
		if !ok {
			return nil, errBad("message_id is required")
		}
		if err := a.be.Recall(ctx, id); err != nil {
			return nil, mapErr(err)
		}
		return map[string]any{}, nil

	case "get_status":
		health, err := a.be.Health(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		return map[string]any{
			"online":          health.Online,
			"good":            health.Online,
			"app_initialized": true,
			"app_enabled":     true,
			"plugins_good":    true,
		}, nil

	case "get_version_info":
		return map[string]any{
			"app_name":         "wxcat",
			"app_version":      Version,
			"protocol_version": "v11",
			"app_full_name":    "WxCat",
		}, nil

	case "can_send_image":
		return map[string]any{"yes": true}, nil
	case "can_send_record":
		return map[string]any{"yes": false}, nil
	case "get_cookies":
		return map[string]any{"cookies": ""}, nil
	case "get_csrf_token":
		return map[string]any{"token": 0}, nil

	case "get_image":
		file := p.str("file")
		if file == "" {
			return nil, errBad("file is required")
		}
		return map[string]any{"file": file, "url": file}, nil

	case "download_file":
		url := p.str("url")
		if url == "" {
			return nil, errBad("url is required")
		}
		data, name, err := a.fetch(url)
		if err != nil {
			return nil, errInternal(err.Error())
		}
		key := fmt.Sprintf("download:%d", time.Now().UnixNano())
		if _, err := a.media.Put(key, data, name); err != nil {
			return nil, errInternal(err.Error())
		}
		path, _ := a.media.Path(key)
		return map[string]any{"file": path, "name": name}, nil

	case "send_group_forward_msg":
		return a.sendForward(ctx, backend.KindGroup, p)
	case "send_private_forward_msg":
		return a.sendForward(ctx, backend.KindPrivate, p)

	case "set_group_ban", "set_group_whole_ban", "set_group_kick", "set_group_card",
		"set_group_leave", "set_group_name", "set_group_admin", "send_like",
		"set_msg_emoji_like", "get_forward_msg", "get_group_honor_info",
		"get_essence_msg_list", "set_essence_msg", "delete_essence_msg",
		"mark_msg_as_read", "set_friend_add_request", "set_group_add_request",
		"get_online_clients", "get_robot_uin_range", "get_group_msg_history",
		"get_friend_msg_history", "get_recent_contact", "get_profile_like":
		return nil, errUnsupported(action + " is not supported by the WeChat backend")

	default:
		return nil, errUnsupported("unknown action: " + action)
	}
}

func (a *Actions) send(ctx context.Context, kind backend.Kind, p params) (any, *ActionError) {
	if kind == "" {
		switch p.str("message_type") {
		case "group":
			kind = backend.KindGroup
		case "private":
			kind = backend.KindPrivate
		default:
			if _, ok := p.raw("group_id"); ok {
				kind = backend.KindGroup
			} else {
				kind = backend.KindPrivate
			}
		}
	}

	var (
		targetID string
		aerr     *ActionError
	)
	if kind == backend.KindGroup {
		targetID, aerr = a.groupParam(p)
	} else {
		targetID, aerr = a.userParam(p)
	}
	if aerr != nil {
		return nil, aerr
	}

	segs, aerr := a.parseSegments(p)
	if aerr != nil {
		return nil, aerr
	}
	id, aerr := a.sendSegments(ctx, backend.Target{Kind: kind, ID: targetID}, segs)
	if aerr != nil {
		return nil, aerr
	}
	return map[string]any{"message_id": id}, nil
}

func (a *Actions) parseSegments(p params) ([]Segment, *ActionError) {
	raw, ok := p.raw("message")
	if !ok {
		return nil, errBad("message is required")
	}
	if p.bool("auto_escape") {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, errBad("auto_escape requires a string message")
		}
		return []Segment{Text(s)}, nil
	}
	segs, err := ParseMessage(raw)
	if err != nil {
		return nil, errBad(err.Error())
	}
	return segs, nil
}

func (a *Actions) sendSegments(ctx context.Context, t backend.Target, segs []Segment) (int64, *ActionError) {
	var (
		text strings.Builder
		ates []string
		last int64
		sent bool
	)

	flushText := func() *ActionError {
		if text.Len() == 0 {
			return nil
		}
		id, err := a.be.SendText(ctx, t, text.String(), ates)
		if err != nil {
			return mapErr(err)
		}
		text.Reset()
		ates = nil
		sent = true
		if id != 0 {
			last = id
		}
		return nil
	}

	for _, seg := range segs {
		switch seg.Type {
		case "text":
			text.WriteString(stringVal(seg.Data["text"]))
		case "at":
			if t.Kind != backend.KindGroup {
				continue
			}
			qq := stringVal(seg.Data["qq"])
			if qq == "all" {
				text.WriteString("@所有人")
				ates = append(ates, "notify@all")
				continue
			}
			wxid, err := a.ids.ResolveUser(seg.Data["qq"])
			if err != nil {
				a.log.Warn("at target unresolved", "qq", qq, "err", err)
				continue
			}
			name := stringVal(seg.Data["name"])
			if name == "" {
				name = "成员"
			}
			text.WriteString("@" + name + " ")
			ates = append(ates, wxid)
		case "face":
			text.WriteString("[表情]")
		case "reply":
			// WeChat has no reply primitive exposed by the backend; drop it.
		case "image":
			if aerr := flushText(); aerr != nil {
				return last, aerr
			}
			data, name, err := a.fetch(stringVal(seg.Data["file"]))
			if err != nil {
				if alt := stringVal(seg.Data["url"]); alt != "" && alt != stringVal(seg.Data["file"]) {
					data, name, err = a.fetch(alt)
				}
			}
			if err != nil {
				return last, errInternal("image: " + err.Error())
			}
			id, err := a.be.SendImage(ctx, t, data, name)
			if err != nil {
				return last, mapErr(err)
			}
			sent = true
			if id != 0 {
				last = id
			}
		case "record", "video", "file":
			if aerr := flushText(); aerr != nil {
				return last, aerr
			}
			ref := stringVal(seg.Data["file"])
			if ref == "" {
				ref = stringVal(seg.Data["url"])
			}
			path, name, err := a.fetchToFile(ref, stringVal(seg.Data["name"]))
			if err != nil {
				if alt := stringVal(seg.Data["url"]); alt != "" && alt != ref {
					path, name, err = a.fetchToFile(alt, stringVal(seg.Data["name"]))
				}
			}
			if err != nil {
				return last, errInternal(seg.Type + ": " + err.Error())
			}
			id, err := a.be.SendFile(ctx, t, path)
			if err != nil {
				_ = os.Remove(path)
				return last, mapErr(err)
			}
			sent = true
			if id != 0 {
				last = id
			}
			_ = name
		default:
			a.log.Debug("ignoring unsupported outbound segment", "type", seg.Type)
		}
	}

	if aerr := flushText(); aerr != nil {
		return last, aerr
	}
	if !sent {
		return 0, errBad("message is empty")
	}
	return last, nil
}

func (a *Actions) sendForward(ctx context.Context, kind backend.Kind, p params) (any, *ActionError) {
	raw, ok := p.raw("messages")
	if !ok {
		return nil, errBad("messages is required")
	}
	var nodes []struct {
		Type string `json:"type"`
		Data struct {
			Content []Segment `json:"content"`
			UserID  any       `json:"user_id"`
			Name    string    `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, errBad("messages must be an array of nodes")
	}

	var lines []string
	for _, n := range nodes {
		if n.Type != "node" {
			continue
		}
		prefix := ""
		if n.Data.Name != "" {
			prefix = n.Data.Name + ": "
		}
		lines = append(lines, prefix+SegmentsToCQ(n.Data.Content))
	}
	if len(lines) == 0 {
		return nil, errBad("no node content")
	}

	segs := []Segment{Text(strings.Join(lines, "\n"))}
	return a.send(ctx, kind, params{"message": mustJSON(segs)})
}

func (a *Actions) renderRecord(rec msgRecord) map[string]any {
	out := map[string]any{
		"message_id":   rec.ID,
		"message_type": string(rec.Kind),
		"user_id":      a.ids.UserID(rec.SenderID),
		"time":         rec.Time,
		"message":      rec.Segments,
		"raw_message":  SegmentsToCQ(rec.Segments),
		"sender": &Sender{
			UserID:   a.ids.UserID(rec.SenderID),
			Nickname: rec.SenderName,
		},
	}
	if rec.Kind == backend.KindGroup {
		out["group_id"] = a.ids.GroupID(rec.TargetID)
	}
	return out
}

func (a *Actions) groupParam(p params) (string, *ActionError) {
	raw, ok := p.raw("group_id")
	if !ok {
		return "", errBad("group_id is required")
	}
	gid, err := a.ids.ResolveGroup(decodeAny(raw))
	if err != nil {
		return "", errBad(err.Error())
	}
	return gid, nil
}

func (a *Actions) userParam(p params) (string, *ActionError) {
	raw, ok := p.raw("user_id")
	if !ok {
		return "", errBad("user_id is required")
	}
	uid, err := a.ids.ResolveUser(decodeAny(raw))
	if err != nil {
		return "", errBad(err.Error())
	}
	return uid, nil
}

// fetch resolves a segment file reference (data URL, http URL, local path or
// raw base64) into bytes.
func (a *Actions) fetch(ref string) ([]byte, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "", errors.New("empty file reference")
	}
	if strings.HasPrefix(ref, "base64://") {
		data, err := base64.StdEncoding.DecodeString(ref[len("base64://"):])
		return data, "file.bin", err
	}
	if strings.HasPrefix(ref, "data:") {
		i := strings.Index(ref, ";base64,")
		if i < 0 {
			return nil, "", errors.New("unsupported data URL")
		}
		data, err := base64.StdEncoding.DecodeString(ref[i+len(";base64,"):])
		return data, "file" + extFromMime(ref[5:i]), err
	}
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		resp, err := a.client.Get(ref)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("download failed: %s", resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload))
		if err != nil {
			return nil, "", err
		}
		name := filepath.Base(ref)
		if i := strings.IndexByte(name, '?'); i >= 0 {
			name = name[:i]
		}
		if name == "" || name == "." || name == "/" {
			name = "file.bin"
		}
		return data, name, nil
	}
	if strings.HasPrefix(ref, "file://") {
		ref = strings.TrimPrefix(ref, "file://")
	}
	if data, err := os.ReadFile(ref); err == nil {
		return data, filepath.Base(ref), nil
	}
	// Last resort: a bare base64 payload.
	if data, err := base64.StdEncoding.DecodeString(ref); err == nil {
		return data, "file.bin", nil
	}
	return nil, "", fmt.Errorf("cannot resolve file reference")
}

func (a *Actions) fetchToFile(ref, name string) (string, string, error) {
	if ref == "" {
		return "", "", errors.New("empty file reference")
	}
	local := strings.TrimPrefix(ref, "file://")
	if !strings.HasPrefix(local, "http") && !strings.HasPrefix(local, "data:") && !strings.HasPrefix(local, "base64://") {
		if _, err := os.Stat(local); err == nil {
			return local, filepath.Base(local), nil
		}
	}
	data, gotName, err := a.fetch(ref)
	if err != nil {
		return "", "", err
	}
	if name == "" {
		name = gotName
	}
	if name == "" || name == "." {
		name = "file.bin"
	}
	path := filepath.Join(a.tmpDir, fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(name)))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", "", err
	}
	return path, name, nil
}

func mapErr(err error) *ActionError {
	switch {
	case errors.Is(err, backend.ErrUnsupported):
		return errUnsupported(err.Error())
	case errors.Is(err, backend.ErrNotLogin):
		return &ActionError{Retcode: RetNoSession, Message: err.Error()}
	case errors.Is(err, backend.ErrNotFound):
		return errNotFound(err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return errInternal(err.Error())
	default:
		return errInternal(err.Error())
	}
}

type params map[string]json.RawMessage

func (p params) raw(k string) (json.RawMessage, bool) {
	v, ok := p[k]
	return v, ok
}

func (p params) str(k string) string {
	v, ok := p[k]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}

func (p params) bool(k string) bool {
	v, ok := p[k]
	if !ok {
		return false
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false
	}
	return b
}

func (p params) int64(k string) (int64, bool) {
	v, ok := p[k]
	if !ok {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(v, &n); err == nil {
		return n, true
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		if n, err := parseInt(s); err == nil {
			return n, true
		}
	}
	return 0, false
}

func decodeAny(raw json.RawMessage) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func parseInt(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func extFromMime(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

// MsgCache keeps the most recent messages so get_msg can answer.
type MsgCache struct {
	mu    sync.Mutex
	max   int
	order []int64
	items map[int64]msgRecord
}

type msgRecord struct {
	ID         int64
	Kind       backend.Kind
	TargetID   string
	SenderID   string
	SenderName string
	Time       int64
	Segments   []Segment
}

// NewMsgCache creates a bounded message cache.
func NewMsgCache(max int) *MsgCache {
	return &MsgCache{max: max, items: make(map[int64]msgRecord)}
}

// Put records a message.
func (c *MsgCache) Put(rec msgRecord) {
	if rec.ID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[rec.ID]; !ok {
		c.order = append(c.order, rec.ID)
	}
	c.items[rec.ID] = rec
	for len(c.order) > c.max {
		old := c.order[0]
		c.order = c.order[1:]
		delete(c.items, old)
	}
}

// Get looks up a message.
func (c *MsgCache) Get(id int64) (msgRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.items[id]
	return rec, ok
}

// RememberInbound stores an inbound event so get_msg works for it too.
func (a *Actions) RememberInbound(ev *Event) {
	if ev == nil || ev.PostType != "message" {
		return
	}
	id, ok := asInt64(ev.MessageID)
	if !ok {
		return
	}
	rec := msgRecord{
		ID:         id,
		SenderName: senderName(ev.Sender),
		Time:       ev.Time,
		Segments:   ev.Segments,
	}
	if ev.GroupID != nil {
		rec.Kind = backend.KindGroup
		if wxid, err := a.ids.ResolveGroup(ev.GroupID); err == nil {
			rec.TargetID = wxid
		}
	} else {
		rec.Kind = backend.KindPrivate
		if wxid, err := a.ids.ResolveUser(ev.UserID); err == nil {
			rec.TargetID = wxid
		}
	}
	if wxid, err := a.ids.ResolveUser(ev.UserID); err == nil {
		rec.SenderID = wxid
	}
	a.cache.Put(rec)
}

func asInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case string:
		n, err := parseInt(t)
		return n, err == nil
	default:
		return 0, false
	}
}

func senderName(s *Sender) string {
	if s == nil {
		return ""
	}
	return s.Nickname
}

// Package rest adapts HTTP bridges such as MimicWX-Linux (WeChat for Linux with
// SQLCipher + UI automation) and generic WeChat hooks to the WxCat backend
// interface.
//
// The dialect is configurable through RestConfig path overrides because these
// bridges are not standardised. Polling is used for incoming messages; a
// WebSocket subscription is used as a low-latency wake-up when available.
package rest

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/wxmsg"
)

// Adapter talks to a REST bridge.
type Adapter struct {
	cfg  config.RestConfig
	name string
	log  *slog.Logger

	events chan backend.RawEvent
	client *http.Client

	mu       sync.RWMutex
	online   bool
	since    time.Time
	self     backend.SelfInfo
	contacts map[string]backend.Contact
	names    map[string]string

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a REST backend adapter.
func New(name string, cfg config.RestConfig, log *slog.Logger) *Adapter {
	applyDefaults(&cfg)
	return &Adapter{
		cfg:      cfg,
		name:     name,
		log:      log,
		events:   make(chan backend.RawEvent, 512),
		client:   &http.Client{Timeout: 20 * time.Second},
		contacts: make(map[string]backend.Contact),
		names:    make(map[string]string),
	}
}

func applyDefaults(c *config.RestConfig) {
	if c.BaseURL == "" {
		c.BaseURL = "http://127.0.0.1:8899"
	}
	if c.StatusPath == "" {
		c.StatusPath = "/status"
	}
	if c.ContactsPath == "" {
		c.ContactsPath = "/contacts"
	}
	if c.MessagesPath == "" {
		c.MessagesPath = "/messages/new"
	}
	if c.SendTextPath == "" {
		c.SendTextPath = "/send"
	}
	if c.SendImagePath == "" {
		c.SendImagePath = "/send_image"
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2000
	}
}

// Name implements backend.Backend.
func (a *Adapter) Name() string { return a.name }

// Start launches the health probe and message poll loop.
func (a *Adapter) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.run(ctx)
	}()
	return nil
}

// Stop shuts the adapter down.
func (a *Adapter) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	return nil
}

// Events implements backend.Backend.
func (a *Adapter) Events() <-chan backend.RawEvent { return a.events }

func (a *Adapter) run(ctx context.Context) {
	interval := time.Duration(a.cfg.PollInterval) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	a.probe(ctx)
	a.poll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.probe(ctx)
			a.poll(ctx)
		}
	}
}

func (a *Adapter) probe(ctx context.Context) {
	body, err := a.get(ctx, a.cfg.StatusPath)
	if err != nil {
		a.setOnline(false)
		return
	}
	var status map[string]any
	_ = json.Unmarshal(body, &status)
	online := true
	if s, ok := status["status"].(string); ok {
		online = s == "ok" || s == "running" || s == "online" || s == "logged_in"
	}
	a.setOnline(online)
}

func (a *Adapter) setOnline(online bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if online && !a.online {
		a.since = time.Now()
	}
	a.online = online
}

func (a *Adapter) poll(ctx context.Context) {
	body, err := a.get(ctx, a.cfg.MessagesPath)
	if err != nil {
		return
	}
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		// Some bridges wrap the list in an object.
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(body, &wrapper); err != nil {
			return
		}
		for _, key := range []string{"messages", "data", "items"} {
			if raw, ok := wrapper[key]; ok {
				_ = json.Unmarshal(raw, &items)
				break
			}
		}
	}

	for _, item := range items {
		ev, ok := a.convert(item)
		if !ok {
			continue
		}
		select {
		case a.events <- ev:
		case <-ctx.Done():
			return
		default:
			a.log.Warn("rest backend event queue full, dropping message", "id", ev.MsgID)
		}
	}
}

// Health implements backend.Backend.
func (a *Adapter) Health(ctx context.Context) (backend.Health, error) {
	a.mu.RLock()
	online, since := a.online, a.since
	a.mu.RUnlock()
	if !online {
		return backend.Health{Online: false, Detail: "bridge unreachable", Since: since}, nil
	}
	return backend.Health{Online: true, Detail: "ok", Since: since}, nil
}

// Self implements backend.Backend.
func (a *Adapter) Self(ctx context.Context) (backend.SelfInfo, error) {
	a.mu.RLock()
	self := a.self
	a.mu.RUnlock()
	if self.ID != "" {
		return self, nil
	}

	// Some bridges (MimicWX) do not expose the logged-in account; the operator
	// supplies it in the config instead.
	if a.cfg.SelfWxid != "" {
		self = backend.SelfInfo{ID: a.cfg.SelfWxid, Name: a.cfg.SelfWxid}
		a.mu.Lock()
		a.self = self
		a.mu.Unlock()
		return self, nil
	}

	body, err := a.get(ctx, a.cfg.StatusPath)
	if err != nil {
		return backend.SelfInfo{}, backend.ErrNotLogin
	}
	var status map[string]any
	_ = json.Unmarshal(body, &status)
	self = backend.SelfInfo{
		ID:   firstString(status, "wxid", "self_wxid", "username", "user_id"),
		Name: firstString(status, "nickname", "name", "self_name"),
	}
	if self.ID == "" {
		return backend.SelfInfo{}, backend.ErrNotLogin
	}
	if self.Name == "" {
		self.Name = self.ID
	}
	a.mu.Lock()
	a.self = self
	a.mu.Unlock()
	return self, nil
}

// Contacts implements backend.Backend.
func (a *Adapter) Contacts(ctx context.Context) ([]backend.Contact, error) {
	body, err := a.get(ctx, a.cfg.ContactsPath)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, err
	}
	out := make([]backend.Contact, 0, len(items))
	index := make(map[string]backend.Contact, len(items))
	names := make(map[string]string, len(items))
	for _, item := range items {
		id := firstString(item, "username", "wxid", "id", "chat")
		if id == "" {
			continue
		}
		name := firstString(item, "name", "nickname", "display_name", "talker_display_name")
		if name == "" {
			name = id
		}
		c := backend.Contact{
			ID:      id,
			Name:    name,
			Remark:  firstString(item, "remark"),
			IsGroup: strings.HasSuffix(id, "@chatroom"),
		}
		out = append(out, c)
		index[id] = c
		names[id] = name
	}
	a.mu.Lock()
	a.contacts = index
	a.names = names
	a.mu.Unlock()
	return out, nil
}

// GroupMembers implements backend.Backend. REST bridges do not expose member
// lists, so this returns an empty slice.
func (a *Adapter) GroupMembers(ctx context.Context, groupID string) ([]backend.Member, error) {
	return []backend.Member{}, nil
}

// SendText implements backend.Backend.
func (a *Adapter) SendText(ctx context.Context, t backend.Target, text string, ats []string) (int64, error) {
	to, err := a.displayName(ctx, t.ID)
	if err != nil {
		return 0, err
	}
	payload := map[string]any{"to": to, "text": text}
	if len(ats) > 0 {
		payload["ats"] = ats
	}
	if err := a.post(ctx, a.cfg.SendTextPath, payload); err != nil {
		return 0, err
	}
	return 0, nil
}

// SendImage implements backend.Backend.
func (a *Adapter) SendImage(ctx context.Context, t backend.Target, data []byte, name string) (int64, error) {
	to, err := a.displayName(ctx, t.ID)
	if err != nil {
		return 0, err
	}
	if name == "" {
		name = "image.png"
	}
	payload := map[string]any{
		"to":   to,
		"file": base64.StdEncoding.EncodeToString(data),
		"name": name,
	}
	if err := a.post(ctx, a.cfg.SendImagePath, payload); err != nil {
		return 0, err
	}
	return 0, nil
}

// SendFile implements backend.Backend.
func (a *Adapter) SendFile(ctx context.Context, t backend.Target, path string) (int64, error) {
	return 0, backend.ErrUnsupported
}

// Recall implements backend.Backend.
func (a *Adapter) Recall(ctx context.Context, msgID int64) error {
	return backend.ErrUnsupported
}

func (a *Adapter) displayName(ctx context.Context, id string) (string, error) {
	a.mu.RLock()
	name, ok := a.names[id]
	a.mu.RUnlock()
	if ok && name != "" {
		return name, nil
	}
	if _, err := a.Contacts(ctx); err != nil {
		return "", err
	}
	a.mu.RLock()
	name, ok = a.names[id]
	a.mu.RUnlock()
	if !ok || name == "" {
		return "", fmt.Errorf("contact %s not found in bridge", id)
	}
	return name, nil
}

func (a *Adapter) convert(item map[string]any) (backend.RawEvent, bool) {
	id := firstInt(item, "local_id", "server_id", "msg_id", "id")
	if id == 0 {
		id = time.Now().UnixNano()
	}
	chat := firstString(item, "chat", "roomid", "session", "conversation")
	talker := firstString(item, "talker", "sender", "from", "user_id")
	if talker == "" {
		talker = chat
	}
	kind := backend.KindPrivate
	if strings.HasSuffix(chat, "@chatroom") {
		kind = backend.KindGroup
	}
	ts := firstInt(item, "create_time", "ts", "timestamp", "time")
	if ts == 0 {
		ts = time.Now().Unix()
	}

	ev := backend.RawEvent{
		Type:       backend.EventMessage,
		MsgID:      id,
		Time:       ts,
		Target:     backend.Target{Kind: kind, ID: chat},
		SenderID:   talker,
		SenderName: firstString(item, "talker_display_name", "sender_name", "name"),
		IsSelf:     firstBool(item, "is_self", "self"),
		XML:        firstString(item, "xml"),
	}
	if ev.Target.ID == "" {
		ev.Target.ID = talker
	}

	msgType := int(firstInt(item, "msg_type", "type"))
	content := firstString(item, "content", "text", "message")
	if kind == backend.KindGroup {
		content = wxmsg.StripGroupPrefix(content, talker)
	}

	switch msgType {
	case 1, 0:
		ev.MsgKind = backend.MsgText
		ev.Text = content
	case 3:
		ev.MsgKind = backend.MsgImage
		ev.MediaName = "image"
	case 34:
		ev.MsgKind = backend.MsgVoice
		ev.MediaName = "voice.silk"
	case 43:
		ev.MsgKind = backend.MsgVideo
		ev.MediaName = "video.mp4"
	case 47:
		ev.MsgKind = backend.MsgFace
		ev.Text = "[表情]"
	case 49:
		if app, ok := wxmsg.ParseApp(ev.XML); ok {
			switch app.AppMsg.Type {
			case wxmsg.AppTypeFile:
				ev.MsgKind = backend.MsgFile
				ev.MediaName = app.FileName()
			case wxmsg.AppTypeLink, 4:
				ev.MsgKind = backend.MsgLink
				ev.Text, _ = app.Link()
			default:
				ev.MsgKind = backend.MsgOther
				ev.Text = app.AppMsg.Title
			}
		} else {
			ev.MsgKind = backend.MsgOther
			ev.Text = content
		}
	case 10000, 10002:
		msgID, ok := wxmsg.ParseRevoke(ev.XML)
		if !ok {
			return backend.RawEvent{}, false
		}
		ev.Type = backend.EventNotice
		ev.MsgID = msgID
		ev.OperatorID = talker
		if kind == backend.KindGroup {
			ev.NoticeType = "group_recall"
		} else {
			ev.NoticeType = "friend_recall"
		}
	default:
		ev.MsgKind = backend.MsgOther
		ev.Text = content
	}
	return ev, true
}

func (a *Adapter) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	a.authorize(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bridge returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func (a *Adapter) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	a.authorize(req)
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("bridge returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (a *Adapter) authorize(req *http.Request) {
	if a.cfg.Token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			case json.Number:
				return t.String()
			case float64:
				return strconv.FormatInt(int64(t), 10)
			}
		}
	}
	return ""
}

func firstInt(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return int64(t)
			case string:
				if n, err := strconv.ParseInt(t, 10, 64); err == nil {
					return n
				}
			case json.Number:
				if n, err := t.Int64(); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func firstBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

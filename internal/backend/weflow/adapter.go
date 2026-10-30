// Package weflowbackend adapts WeFlow (local WeChat database reader with an
// HTTP API and SSE push) to the WxCat backend interface.
//
// WeFlow has no send API. Sending is delegated either to the Windows UI
// Automation helper (sendMode "uia") or to an external HTTP sender
// (sendMode "http").
package weflowbackend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
)

const maxMedia = 64 << 20

type Adapter struct {
	cfg config.WeFlowConfig
	log *slog.Logger

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

// New creates a WeFlow backend.
func New(cfg config.WeFlowConfig, log *slog.Logger) *Adapter {
	return &Adapter{
		cfg:      cfg,
		log:      log,
		events:   make(chan backend.RawEvent, 512),
		client:   &http.Client{Timeout: 30 * time.Second},
		contacts: make(map[string]backend.Contact),
		names:    make(map[string]string),
	}
}

func (a *Adapter) Name() string { return "weflow" }

func (a *Adapter) Start(ctx context.Context) error {
	if a.cfg.SelfWxid == "" {
		a.log.Warn("weflow.selfWxid is empty; get_login_info will fail until it is set")
	}
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.mu.Lock()
	a.since = time.Now()
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.run(ctx)
	}()
	return nil
}

func (a *Adapter) Stop() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	return nil
}

func (a *Adapter) Events() <-chan backend.RawEvent { return a.events }

func (a *Adapter) run(ctx context.Context) {
	a.probe(ctx)
	go a.pollLoop(ctx)
	a.sseLoop(ctx)
}

func (a *Adapter) probe(ctx context.Context) {
	_, err := a.get(ctx, "/health")
	a.setOnline(err == nil)
}

func (a *Adapter) setOnline(online bool) {
	a.mu.Lock()
	if online && !a.online {
		a.since = time.Now()
	}
	a.online = online
	a.mu.Unlock()
}

// ---------- SSE ----------

func (a *Adapter) sseLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := a.sseOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		a.log.Warn("weflow SSE disconnected", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (a *Adapter) sseOnce(ctx context.Context) error {
	endpoint := a.cfg.BaseURL + "/api/v1/push/messages"
	if a.cfg.Token != "" {
		endpoint += "?access_token=" + url.QueryEscape(a.cfg.Token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if a.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}

	resp, err := (&http.Client{Timeout: 0}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE returned %s", resp.Status)
	}
	a.setOnline(true)
	a.log.Info("weflow SSE connected")

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var event string
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if data.Len() > 0 {
				a.handlePush(ctx, event, []byte(data.String()))
				data.Reset()
			}
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

type pushEvent struct {
	Event       string `json:"event"`
	SessionID   string `json:"sessionId"`
	SessionType string `json:"sessionType"`
	RawID       string `json:"rawid"`
	SourceName  string `json:"sourceName"`
	GroupName   string `json:"groupName"`
	Content     string `json:"content"`
	Timestamp   int64  `json:"timestamp"`
}

func (a *Adapter) handlePush(ctx context.Context, event string, data []byte) {
	var push pushEvent
	if err := json.Unmarshal(data, &push); err != nil {
		a.log.Warn("weflow SSE payload is not JSON", "err", err)
		return
	}
	if push.Event == "" {
		push.Event = event
	}
	if push.SessionID == "" {
		return
	}

	switch push.Event {
	case "message.revoke":
		msgID, _ := strconv.ParseInt(push.RawID, 10, 64)
		ev := backend.RawEvent{
			Type:   backend.EventNotice,
			MsgID:  msgID,
			Time:   push.Timestamp,
			Target: backend.Target{Kind: targetKind(push.SessionID), ID: push.SessionID},
			NoticeType: func() string {
				if targetKind(push.SessionID) == backend.KindGroup {
					return "group_recall"
				}
				return "friend_recall"
			}(),
		}
		a.emit(ctx, ev)
	case "message.new", "":
		a.handleNewMessage(ctx, push)
	}
}

func (a *Adapter) handleNewMessage(ctx context.Context, push pushEvent) {
	msgs, err := a.fetchMessages(ctx, push.SessionID, 50)
	if err != nil {
		a.log.Warn("weflow message lookup failed", "session", push.SessionID, "err", err)
		return
	}
	var matched *wfMessage
	for i := range msgs {
		if msgs[i].ServerID == push.RawID || strconv.FormatInt(msgs[i].LocalID, 10) == push.RawID {
			matched = &msgs[i]
			break
		}
	}
	if matched == nil && len(msgs) > 0 {
		// The push may arrive before the row is committed; use the newest row.
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].CreateTime < msgs[j].CreateTime })
		matched = &msgs[len(msgs)-1]
	}
	if matched == nil {
		return
	}
	if ev, ok := a.toRaw(push.SessionID, *matched); ok {
		a.emit(ctx, ev)
	}
}

// ---------- polling fallback ----------

func (a *Adapter) pollLoop(ctx context.Context) {
	interval := time.Duration(a.cfg.PollInterval) * time.Millisecond
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pollOnce(ctx)
		}
	}
}

func (a *Adapter) pollOnce(ctx context.Context) {
	sessions, err := a.fetchSessions(ctx)
	if err != nil {
		a.setOnline(false)
		return
	}
	a.setOnline(true)
	for _, s := range sessions {
		if s.UnreadCount <= 0 {
			continue
		}
		msgs, err := a.fetchMessages(ctx, s.Username, 50)
		if err != nil {
			continue
		}
		sort.Slice(msgs, func(i, j int) bool { return msgs[i].CreateTime < msgs[j].CreateTime })
		a.mu.RLock()
		since := a.since.Unix() - 30
		a.mu.RUnlock()
		for _, m := range msgs {
			if m.CreateTime < since {
				continue
			}
			if ev, ok := a.toRaw(s.Username, m); ok {
				a.emit(ctx, ev)
			}
		}
	}
}

// ---------- data model ----------

type wfMessage struct {
	LocalID        int64  `json:"localId"`
	ServerID       string `json:"serverId"`
	LocalType      int64  `json:"localType"`
	CreateTime     int64  `json:"createTime"`
	IsSend         int    `json:"isSend"`
	SenderUsername string `json:"senderUsername"`
	Content        string `json:"content"`
	RawContent     string `json:"rawContent"`
	ParsedContent  string `json:"parsedContent"`
	MediaType      string `json:"mediaType"`
	MediaFileName  string `json:"mediaFileName"`
	MediaURL       string `json:"mediaUrl"`
	MediaLocalPath string `json:"mediaLocalPath"`
}

type wfContact struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Remark      string `json:"remark"`
	Nickname    string `json:"nickname"`
	Type        string `json:"type"`
}

type wfSession struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Type        int    `json:"type"`
	UnreadCount int    `json:"unreadCount"`
}

type wfMember struct {
	Wxid          string `json:"wxid"`
	DisplayName   string `json:"displayName"`
	Nickname      string `json:"nickname"`
	Remark        string `json:"remark"`
	GroupNickname string `json:"groupNickname"`
	IsOwner       bool   `json:"isOwner"`
}

func (a *Adapter) toRaw(talker string, m wfMessage) (backend.RawEvent, bool) {
	msgID, err := strconv.ParseInt(m.ServerID, 10, 64)
	if err != nil || msgID == 0 {
		msgID = m.LocalID
	}
	if msgID == 0 {
		return backend.RawEvent{}, false
	}
	kind := targetKind(talker)
	ev := backend.RawEvent{
		Type:     backend.EventMessage,
		MsgID:    msgID,
		Time:     m.CreateTime,
		Target:   backend.Target{Kind: kind, ID: talker},
		SenderID: m.SenderUsername,
		IsSelf:   m.IsSend == 1,
		XML:      m.RawContent,
	}
	ev.SenderName = a.lookupName(m.SenderUsername)
	if ev.SenderName == "" {
		ev.SenderName = m.SenderUsername
	}

	text := strings.TrimSpace(m.ParsedContent)
	if text == "" {
		text = strings.TrimSpace(m.Content)
	}

	switch strings.ToLower(m.MediaType) {
	case "image":
		ev.MsgKind = backend.MsgImage
		ev.MediaName = firstNonEmpty(m.MediaFileName, "image")
	case "voice":
		ev.MsgKind = backend.MsgVoice
		ev.MediaName = firstNonEmpty(m.MediaFileName, "voice.silk")
	case "video":
		ev.MsgKind = backend.MsgVideo
		ev.MediaName = firstNonEmpty(m.MediaFileName, "video.mp4")
	case "emoji":
		ev.MsgKind = backend.MsgFace
		ev.Text = "[表情]"
	case "file":
		ev.MsgKind = backend.MsgFile
		ev.MediaName = firstNonEmpty(m.MediaFileName, "file.bin")
	default:
		ev.MsgKind = backend.MsgText
		ev.Text = text
		if ev.Text == "" {
			ev.Text = "[暂不支持的消息类型]"
		}
	}
	if ev.MediaName != "" || m.MediaURL != "" || m.MediaLocalPath != "" {
		ev.MediaPath = m.MediaLocalPath
		ev.MediaLoader = a.mediaLoader(m)
	}
	return ev, true
}

func (a *Adapter) mediaLoader(m wfMessage) func() ([]byte, error) {
	return func() ([]byte, error) {
		if m.MediaLocalPath != "" {
			if data, err := os.ReadFile(m.MediaLocalPath); err == nil {
				return data, nil
			}
		}
		if m.MediaURL == "" {
			return nil, errors.New("no media URL")
		}
		req, err := http.NewRequest(http.MethodGet, m.MediaURL, nil)
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
			return nil, fmt.Errorf("media returned %s", resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, maxMedia))
	}
}

func (a *Adapter) emit(ctx context.Context, ev backend.RawEvent) {
	select {
	case a.events <- ev:
	case <-ctx.Done():
	default:
		a.log.Warn("weflow event queue full, dropping message", "msg_id", ev.MsgID)
	}
}

// ---------- backend.Backend ----------

func (a *Adapter) Health(ctx context.Context) (backend.Health, error) {
	_, err := a.get(ctx, "/health")
	online := err == nil
	a.setOnline(online)
	a.mu.RLock()
	since := a.since
	a.mu.RUnlock()
	return backend.Health{Online: online, Detail: errString(err), Since: since}, nil
}

func (a *Adapter) Self(ctx context.Context) (backend.SelfInfo, error) {
	if a.cfg.SelfWxid == "" {
		return backend.SelfInfo{}, fmt.Errorf("%w: set backend.weflow.selfWxid", backend.ErrNotLogin)
	}
	a.mu.RLock()
	self := a.self
	a.mu.RUnlock()
	if self.ID != "" {
		return self, nil
	}
	name := a.lookupName(a.cfg.SelfWxid)
	if name == "" {
		name = a.cfg.SelfWxid
	}
	self = backend.SelfInfo{ID: a.cfg.SelfWxid, Name: name}
	a.mu.Lock()
	a.self = self
	a.mu.Unlock()
	return self, nil
}

func (a *Adapter) Contacts(ctx context.Context) ([]backend.Contact, error) {
	var out []backend.Contact
	index := make(map[string]backend.Contact)
	names := make(map[string]string)

	body, err := a.get(ctx, "/api/v1/contacts?limit=1000")
	if err != nil {
		return nil, err
	}
	var contacts struct {
		Contacts []wfContact `json:"contacts"`
	}
	if err := json.Unmarshal(body, &contacts); err != nil {
		return nil, err
	}
	for _, c := range contacts.Contacts {
		if c.Username == "" {
			continue
		}
		name := firstNonEmpty(c.DisplayName, c.Remark, c.Nickname, c.Username)
		item := backend.Contact{
			ID:      c.Username,
			Name:    name,
			Remark:  c.Remark,
			IsGroup: targetKind(c.Username) == backend.KindGroup,
		}
		out = append(out, item)
		index[item.ID] = item
		names[item.ID] = item.Name
	}

	sessions, err := a.fetchSessions(ctx)
	if err == nil {
		for _, s := range sessions {
			if targetKind(s.Username) != backend.KindGroup {
				continue
			}
			if _, ok := index[s.Username]; ok {
				continue
			}
			name := firstNonEmpty(s.DisplayName, s.Username)
			item := backend.Contact{ID: s.Username, Name: name, IsGroup: true}
			out = append(out, item)
			index[item.ID] = item
			names[item.ID] = name
		}
	}

	a.mu.Lock()
	a.contacts = index
	a.names = names
	a.mu.Unlock()
	return out, nil
}

func (a *Adapter) GroupMembers(ctx context.Context, groupID string) ([]backend.Member, error) {
	body, err := a.get(ctx, "/api/v1/group-members?chatroomId="+url.QueryEscape(groupID))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Members []wfMember `json:"members"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := make([]backend.Member, 0, len(resp.Members))
	for _, m := range resp.Members {
		role := "member"
		if m.IsOwner {
			role = "owner"
		}
		out = append(out, backend.Member{
			ID:   m.Wxid,
			Name: firstNonEmpty(m.DisplayName, m.Nickname, m.Remark, m.Wxid),
			Card: m.GroupNickname,
			Role: role,
		})
	}
	return out, nil
}

func (a *Adapter) SendText(ctx context.Context, t backend.Target, text string, ats []string) (int64, error) {
	if len(ats) > 0 {
		a.log.Debug("weflow uia send does not create native @ mentions", "ats", ats)
	}
	switch strings.ToLower(a.cfg.SendMode) {
	case "http":
		return 0, a.sendHTTP(ctx, map[string]any{
			"to":   t.ID,
			"text": text,
			"ats":  ats,
		})
	case "uia":
		name, err := a.displayName(ctx, t.ID)
		if err != nil {
			return 0, err
		}
		return 0, a.runUIA(ctx, name, text, "")
	case "none", "":
		return 0, backend.ErrUnsupported
	default:
		return 0, fmt.Errorf("unknown weflow sendMode %q", a.cfg.SendMode)
	}
}

func (a *Adapter) SendImage(ctx context.Context, t backend.Target, data []byte, name string) (int64, error) {
	switch strings.ToLower(a.cfg.SendMode) {
	case "http":
		return 0, a.sendHTTP(ctx, map[string]any{
			"to":    t.ID,
			"image": base64.StdEncoding.EncodeToString(data),
			"name":  name,
		})
	case "uia":
		dir := filepath.Join(os.TempDir(), "wxcat-weflow")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, err
		}
		path := filepath.Join(dir, fmt.Sprintf("%d_%s", time.Now().UnixNano(), sanitize(name)))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return 0, err
		}
		defer os.Remove(path)
		target, err := a.displayName(ctx, t.ID)
		if err != nil {
			return 0, err
		}
		return 0, a.runUIA(ctx, target, "", path)
	case "none", "":
		return 0, backend.ErrUnsupported
	default:
		return 0, fmt.Errorf("unknown weflow sendMode %q", a.cfg.SendMode)
	}
}

func (a *Adapter) SendFile(ctx context.Context, t backend.Target, path string) (int64, error) {
	switch strings.ToLower(a.cfg.SendMode) {
	case "http":
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		return 0, a.sendHTTP(ctx, map[string]any{
			"to":   t.ID,
			"file": base64.StdEncoding.EncodeToString(data),
			"name": filepath.Base(path),
		})
	case "uia":
		target, err := a.displayName(ctx, t.ID)
		if err != nil {
			return 0, err
		}
		return 0, a.runUIA(ctx, target, "", path)
	case "none", "":
		return 0, backend.ErrUnsupported
	default:
		return 0, fmt.Errorf("unknown weflow sendMode %q", a.cfg.SendMode)
	}
}

func (a *Adapter) Recall(ctx context.Context, msgID int64) error {
	return backend.ErrUnsupported
}

// ---------- helpers ----------

func (a *Adapter) displayName(ctx context.Context, id string) (string, error) {
	a.mu.RLock()
	name := a.names[id]
	a.mu.RUnlock()
	if name != "" {
		return name, nil
	}
	if _, err := a.Contacts(ctx); err != nil {
		return "", err
	}
	a.mu.RLock()
	name = a.names[id]
	a.mu.RUnlock()
	if name == "" {
		return "", fmt.Errorf("contact %s not found in WeFlow", id)
	}
	return name, nil
}

func (a *Adapter) lookupName(id string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.names[id]
}

func (a *Adapter) sendHTTP(ctx context.Context, payload map[string]any) error {
	if a.cfg.SenderURL == "" {
		return errors.New("weflow.sendMode=http but senderUrl is empty")
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.SenderURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.SenderToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.SenderToken)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sender returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func (a *Adapter) runUIA(ctx context.Context, target, text, file string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("%w: uia send requires Windows", backend.ErrUnsupported)
	}
	script := a.cfg.UiaScript
	if script == "" {
		script = "scripts/windows/uia-send.ps1"
	}
	if !filepath.IsAbs(script) {
		abs, err := filepath.Abs(script)
		if err != nil {
			return err
		}
		script = abs
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("uia script not found: %w", err)
	}

	args := []string{"-Sta", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script, "-TargetName", target}
	var textFile string
	if text != "" {
		f, err := os.CreateTemp("", "wxcat-uia-*.txt")
		if err != nil {
			return err
		}
		textFile = f.Name()
		if _, err := f.WriteString(text); err != nil {
			f.Close()
			os.Remove(textFile)
			return err
		}
		f.Close()
		defer os.Remove(textFile)
		args = append(args, "-TextFile", textFile)
	}
	if file != "" {
		args = append(args, "-FilePath", file)
	}

	sendCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(sendCtx, "powershell.exe", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uia send failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (a *Adapter) fetchSessions(ctx context.Context) ([]wfSession, error) {
	body, err := a.get(ctx, "/api/v1/sessions?limit=1000")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Sessions []wfSession `json:"sessions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

func (a *Adapter) fetchMessages(ctx context.Context, talker string, limit int) ([]wfMessage, error) {
	path := fmt.Sprintf("/api/v1/messages?talker=%s&limit=%d", url.QueryEscape(talker), limit)
	body, err := a.get(ctx, path)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Messages []wfMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	return resp.Messages, nil
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
		return nil, fmt.Errorf("weflow returned %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

func (a *Adapter) authorize(req *http.Request) {
	if a.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	}
}

func targetKind(id string) backend.Kind {
	if strings.HasSuffix(id, "@chatroom") {
		return backend.KindGroup
	}
	return backend.KindPrivate
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func sanitize(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return "file.bin"
	}
	return name
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

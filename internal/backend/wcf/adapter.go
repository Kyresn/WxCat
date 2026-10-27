// Package wcfbackend adapts WeChatFerry (running inside Wine) to the WxCat
// Backend interface.
package wcfbackend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/wcfpath"
	"wxcat/internal/wxmsg"
	wcf "wxcat/third_party/wcf"
)

const reconnectDelay = 3 * time.Second

// Adapter talks to the WeChatFerry RPC ports exposed by the injected hook.
type Adapter struct {
	cfg config.WCFConfig
	log *slog.Logger

	events chan backend.RawEvent
	tmpDir string

	mediaClient *http.Client

	mu         sync.RWMutex
	client     *wcf.Client
	online     bool
	since      time.Time
	self       backend.SelfInfo
	contacts   map[string]backend.Contact
	contactsAt time.Time

	memberQuery atomic.Value // string

	seq    atomic.Uint64
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a WeChatFerry backend adapter.
func New(cfg config.WCFConfig, log *slog.Logger) (*Adapter, error) {
	tmp := cfg.DecryptDir
	if tmp == "" {
		tmp = filepath.Join(os.TempDir(), "wxcat-wcf")
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, fmt.Errorf("create wcf temp dir: %w", err)
	}
	return &Adapter{
		cfg:         cfg,
		log:         log,
		events:      make(chan backend.RawEvent, 512),
		tmpDir:      tmp,
		mediaClient: &http.Client{Timeout: 60 * time.Second},
		contacts:    make(map[string]backend.Contact),
	}, nil
}

// Name implements backend.Backend.
func (a *Adapter) Name() string { return "wcf" }

// Start launches the connect/login/receive loop.
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
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	a.mu.Lock()
	client := a.client
	a.client = nil
	a.mu.Unlock()
	if client != nil {
		return client.Close()
	}
	return nil
}

// Events implements backend.Backend.
func (a *Adapter) Events() <-chan backend.RawEvent { return a.events }

func (a *Adapter) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		client, err := wcf.New(a.cfg.CmdAddr, a.cfg.MsgAddr)
		if err != nil {
			a.log.Warn("wcf connect failed, is the hook injected?", "err", err)
			if !sleep(ctx, reconnectDelay) {
				return
			}
			continue
		}

		a.mu.Lock()
		a.client = client
		a.mu.Unlock()

		if !a.waitLogin(ctx, client) {
			a.dropClient(client)
			continue
		}

		self, err := a.loadSelf(client)
		if err != nil {
			a.log.Warn("wcf get self info failed", "err", err)
			a.dropClient(client)
			continue
		}
		a.mu.Lock()
		a.self = self
		a.online = true
		a.since = time.Now()
		a.mu.Unlock()
		a.log.Info("wechat logged in", "wxid", self.ID, "nickname", self.Name)

		if _, err := client.EnableRecv(true); err != nil {
			a.log.Warn("wcf enable receive failed", "err", err)
		}
		// Warm the contact cache so message events carry nicknames.
		if _, err := a.Contacts(ctx); err != nil {
			a.log.Debug("wcf contact prefetch failed", "err", err)
		}

		err = client.OnMSG(ctx, func(m *wcf.WxMsg) {
			ev, ok := a.convert(client, m)
			if !ok {
				return
			}
			select {
			case a.events <- ev:
			case <-ctx.Done():
			default:
				a.log.Warn("backend event queue full, dropping message", "msg_id", m.Id)
			}
		})
		if err != nil && ctx.Err() == nil {
			a.log.Warn("wcf receive loop ended", "err", err)
		}

		a.mu.Lock()
		a.online = false
		a.mu.Unlock()
		a.dropClient(client)

		if ctx.Err() != nil {
			return
		}
		if !sleep(ctx, reconnectDelay) {
			return
		}
	}
}

func (a *Adapter) waitLogin(ctx context.Context, client *wcf.Client) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		ok, err := client.IsLogin()
		if err != nil {
			a.log.Warn("wcf login probe failed", "err", err)
			return false
		}
		if ok {
			return true
		}
		a.log.Info("waiting for wechat login (scan the QR code in noVNC)")
		if !sleep(ctx, 3*time.Second) {
			return false
		}
	}
}

func (a *Adapter) dropClient(client *wcf.Client) {
	a.mu.Lock()
	if a.client == client {
		a.client = nil
		a.online = false
	}
	a.mu.Unlock()
	_ = client.Close()
}

func (a *Adapter) loadSelf(client *wcf.Client) (backend.SelfInfo, error) {
	ui, err := client.UserInfo()
	if err == nil && ui != nil && ui.Wxid != "" {
		return backend.SelfInfo{ID: ui.Wxid, Name: ui.Name}, nil
	}
	wxid, err := client.SelfWXID()
	if err != nil {
		return backend.SelfInfo{}, err
	}
	if wxid == "" {
		return backend.SelfInfo{}, errors.New("empty wxid")
	}
	return backend.SelfInfo{ID: wxid, Name: wxid}, nil
}

// Health implements backend.Backend.
func (a *Adapter) Health(ctx context.Context) (backend.Health, error) {
	a.mu.RLock()
	client := a.client
	online := a.online
	since := a.since
	a.mu.RUnlock()

	if client == nil {
		return backend.Health{Online: false, Detail: "not connected", Since: since}, nil
	}
	ok, err := client.IsLogin()
	if err != nil {
		return backend.Health{Online: false, Detail: err.Error(), Since: since}, nil
	}
	if !ok {
		return backend.Health{Online: false, Detail: "not logged in", Since: since}, nil
	}
	a.mu.Lock()
	a.online = true
	a.mu.Unlock()
	return backend.Health{Online: online || ok, Detail: "ok", Since: since}, nil
}

// Self implements backend.Backend.
func (a *Adapter) Self(ctx context.Context) (backend.SelfInfo, error) {
	a.mu.RLock()
	self := a.self
	client := a.client
	a.mu.RUnlock()
	if self.ID != "" {
		return self, nil
	}
	if client == nil {
		return backend.SelfInfo{}, backend.ErrNotLogin
	}
	info, err := a.loadSelf(client)
	if err != nil {
		return backend.SelfInfo{}, backend.ErrNotLogin
	}
	a.mu.Lock()
	a.self = info
	a.mu.Unlock()
	return info, nil
}

// Contacts implements backend.Backend.
func (a *Adapter) Contacts(ctx context.Context) ([]backend.Contact, error) {
	client := a.getClient()
	if client == nil {
		return nil, backend.ErrNotLogin
	}
	raw, err := client.Contacts()
	if err != nil {
		return nil, err
	}
	out := make([]backend.Contact, 0, len(raw))
	index := make(map[string]backend.Contact, len(raw))
	for _, c := range raw {
		if c == nil || c.Wxid == "" {
			continue
		}
		contact := backend.Contact{
			ID:      c.Wxid,
			Name:    c.Name,
			Remark:  c.Remark,
			IsGroup: strings.HasSuffix(c.Wxid, "@chatroom"),
		}
		if contact.Name == "" {
			contact.Name = contact.Remark
		}
		out = append(out, contact)
		index[contact.ID] = contact
	}
	a.mu.Lock()
	a.contacts = index
	a.contactsAt = time.Now()
	a.mu.Unlock()
	return out, nil
}

// GroupMembers implements backend.Backend. WeChatFerry has no member-list RPC,
// so this is a best-effort read from the contact database. An empty list is
// returned when the schema cannot be recognised.
func (a *Adapter) GroupMembers(ctx context.Context, groupID string) ([]backend.Member, error) {
	client := a.getClient()
	if client == nil {
		return nil, backend.ErrNotLogin
	}

	queries := []string{}
	if cached, ok := a.memberQuery.Load().(string); ok && cached != "" {
		queries = append(queries, fmt.Sprintf(cached, escapeSQL(groupID)))
	}
	queries = append(queries,
		fmt.Sprintf("SELECT * FROM ChatRoom WHERE UserName='%s'", escapeSQL(groupID)),
		fmt.Sprintf("SELECT * FROM chatroom WHERE username='%s'", escapeSQL(groupID)),
		fmt.Sprintf("SELECT * FROM ChatRoomInfo WHERE UserName='%s'", escapeSQL(groupID)),
		fmt.Sprintf("SELECT * FROM Contact WHERE UserName='%s'", escapeSQL(groupID)),
	)

	for _, q := range queries {
		rows, err := client.Query("MicroMsg.db", q)
		if err != nil || len(rows) == 0 {
			continue
		}
		members := parseMemberRows(rows, a.lookupName)
		if len(members) > 0 {
			a.memberQuery.Store(strings.Replace(q, escapeSQL(groupID), "%s", 1))
			return members, nil
		}
	}
	return []backend.Member{}, nil
}

func parseMemberRows(rows []*wcf.DbRow, lookup func(string) string) []backend.Member {
	var members []backend.Member
	seen := make(map[string]struct{})
	for _, row := range rows {
		for _, f := range row.GetFields() {
			value := strings.TrimSpace(string(f.GetContent()))
			if !strings.Contains(value, "wxid_") || len(value) < 8 {
				continue
			}
			for _, part := range strings.FieldsFunc(value, func(r rune) bool {
				return r == ';' || r == ',' || r == '\n' || r == '\x00'
			}) {
				part = strings.TrimSpace(part)
				if !strings.HasPrefix(part, "wxid_") {
					continue
				}
				if _, ok := seen[part]; ok {
					continue
				}
				seen[part] = struct{}{}
				members = append(members, backend.Member{ID: part, Name: lookup(part)})
			}
		}
	}
	return members
}

func (a *Adapter) lookupName(wxid string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if c, ok := a.contacts[wxid]; ok && c.Name != "" {
		return c.Name
	}
	return wxid
}

// SendText implements backend.Backend.
func (a *Adapter) SendText(ctx context.Context, t backend.Target, text string, ats []string) (int64, error) {
	client := a.getClient()
	if client == nil {
		return 0, backend.ErrNotLogin
	}
	status, err := client.SendTxt(text, t.ID, ats)
	if err != nil {
		return 0, err
	}
	if status != 0 {
		return 0, fmt.Errorf("wechat rejected the text message (status %d)", status)
	}
	return 0, nil
}

// SendImage implements backend.Backend.
func (a *Adapter) SendImage(ctx context.Context, t backend.Target, data []byte, name string) (int64, error) {
	client := a.getClient()
	if client == nil {
		return 0, backend.ErrNotLogin
	}

	path, err := a.stageMedia(ctx, data, name)
	if err != nil {
		return 0, err
	}
	status, err := client.SendIMG(path, t.ID)
	if err != nil {
		return 0, err
	}
	if status != 0 {
		return 0, fmt.Errorf("wechat rejected the image (status %d)", status)
	}
	return 0, nil
}

// SendFile implements backend.Backend.
func (a *Adapter) SendFile(ctx context.Context, t backend.Target, path string) (int64, error) {
	client := a.getClient()
	if client == nil {
		return 0, backend.ErrNotLogin
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("file not found: %w", err)
	}

	if a.cfg.MediaURL != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		remote, err := a.uploadRemote(ctx, data, filepath.Base(path))
		if err != nil {
			return 0, err
		}
		status, err := client.SendFile(remote, t.ID)
		if err != nil {
			return 0, err
		}
		if status != 0 {
			return 0, fmt.Errorf("wechat rejected the file (status %d)", status)
		}
		return 0, nil
	}

	status, err := client.SendFile(wcfpath.ToWine(path), t.ID)
	if err != nil {
		return 0, err
	}
	if status != 0 {
		return 0, fmt.Errorf("wechat rejected the file (status %d)", status)
	}
	return 0, nil
}

// stageMedia makes image bytes available to the WeChat host and returns the
// path to pass to WeChatFerry. In remote mode the bytes are uploaded to the
// wcf-agent running next to WeChat.
func (a *Adapter) stageMedia(ctx context.Context, data []byte, name string) (string, error) {
	if a.cfg.MediaURL != "" {
		return a.uploadRemote(ctx, data, name)
	}
	path, err := a.writeTemp(data, name)
	if err != nil {
		return "", err
	}
	return wcfpath.ToWine(path), nil
}

func (a *Adapter) uploadRemote(ctx context.Context, data []byte, name string) (string, error) {
	if name == "" {
		name = "file.bin"
	}
	endpoint := strings.TrimRight(a.cfg.MediaURL, "/") + "/upload?name=" + url.QueryEscape(filepath.Base(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if a.cfg.MediaToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.MediaToken)
	}
	resp, err := a.mediaClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload to wcf-agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("wcf-agent upload failed: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("decode wcf-agent response: %w", err)
	}
	if out.Path == "" {
		return "", errors.New("wcf-agent returned an empty path")
	}
	return out.Path, nil
}

func (a *Adapter) decryptRemote(ctx context.Context, src string) ([]byte, error) {
	endpoint := strings.TrimRight(a.cfg.MediaURL, "/") + "/decrypt"
	body, _ := json.Marshal(map[string]string{"src": src})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.MediaToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.MediaToken)
	}
	resp, err := a.mediaClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("decrypt via wcf-agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("wcf-agent decrypt failed: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// Recall implements backend.Backend. WeChatFerry exposes no recall primitive.
func (a *Adapter) Recall(ctx context.Context, msgID int64) error {
	return backend.ErrUnsupported
}

func (a *Adapter) getClient() *wcf.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

func (a *Adapter) writeTemp(data []byte, name string) (string, error) {
	if name == "" {
		name = "file.bin"
	}
	path := filepath.Join(a.tmpDir, fmt.Sprintf("out_%d_%s", a.seq.Add(1), filepath.Base(name)))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (a *Adapter) convert(client *wcf.Client, m *wcf.WxMsg) (backend.RawEvent, bool) {
	kind := backend.KindPrivate
	if m.IsGroup {
		kind = backend.KindGroup
	}

	targetID := m.Roomid
	if targetID == "" {
		targetID = m.Sender
	}

	ev := backend.RawEvent{
		Type:       backend.EventMessage,
		MsgID:      int64(m.Id & 0x7FFF_FFFF_FFFF_FFFF),
		Time:       int64(m.Ts),
		Target:     backend.Target{Kind: kind, ID: targetID},
		SenderID:   m.Sender,
		SenderName: a.lookupName(m.Sender),
		IsSelf:     m.IsSelf,
		XML:        m.Xml,
	}
	if ev.Time == 0 {
		ev.Time = time.Now().Unix()
	}

	content := m.Content
	if kind == backend.KindGroup && !m.IsSelf {
		content = wxmsg.StripGroupPrefix(content, m.Sender)
	}

	switch m.Type {
	case 1:
		ev.MsgKind = backend.MsgText
		ev.Text = content
	case 3:
		ev.MsgKind = backend.MsgImage
		ev.MediaPath = m.Extra
		ev.MediaName = "image"
		ev.MediaLoader = a.mediaLoader(client, m.Id, m.Extra)
	case 34:
		ev.MsgKind = backend.MsgVoice
		ev.MediaPath = m.Extra
		ev.MediaName = "voice.silk"
		ev.MediaLoader = a.mediaLoader(client, m.Id, m.Extra)
	case 43:
		ev.MsgKind = backend.MsgVideo
		ev.MediaPath = m.Extra
		ev.MediaName = "video.mp4"
		ev.MediaLoader = a.mediaLoader(client, m.Id, m.Extra)
	case 47:
		ev.MsgKind = backend.MsgFace
		ev.Text = "[表情]"
	case 49:
		a.convertApp(&ev, m)
	case 10000, 10002:
		return a.convertSystem(m, ev)
	default:
		ev.MsgKind = backend.MsgOther
		ev.Text = content
	}

	if ev.MsgKind == "" {
		return backend.RawEvent{}, false
	}
	return ev, true
}

func (a *Adapter) convertApp(ev *backend.RawEvent, m *wcf.WxMsg) {
	app, ok := wxmsg.ParseApp(m.Xml)
	if !ok {
		ev.MsgKind = backend.MsgOther
		ev.Text = m.Content
		return
	}
	switch app.AppMsg.Type {
	case wxmsg.AppTypeFile:
		ev.MsgKind = backend.MsgFile
		ev.MediaName = app.FileName()
		ev.MediaPath = m.Extra
	case wxmsg.AppTypeLink, 4:
		ev.MsgKind = backend.MsgLink
		ev.Text, _ = app.Link()
	case wxmsg.AppTypeQuote:
		ev.MsgKind = backend.MsgText
		ev.Text = app.AppMsg.Title
	case wxmsg.AppTypeEmoji:
		ev.MsgKind = backend.MsgFace
		ev.Text = "[表情]"
	default:
		ev.MsgKind = backend.MsgOther
		ev.Text = strings.TrimSpace(app.AppMsg.Title)
		if ev.Text == "" {
			ev.Text = m.Content
		}
	}
}

func (a *Adapter) convertSystem(m *wcf.WxMsg, ev backend.RawEvent) (backend.RawEvent, bool) {
	msgID, ok := wxmsg.ParseRevoke(m.Xml)
	if !ok {
		return backend.RawEvent{}, false
	}
	ev.Type = backend.EventNotice
	ev.MsgID = msgID
	ev.OperatorID = m.Sender
	ev.SenderID = m.Sender
	if m.IsGroup {
		ev.NoticeType = "group_recall"
	} else {
		ev.NoticeType = "friend_recall"
	}
	return ev, true
}

func (a *Adapter) mediaLoader(client *wcf.Client, msgID uint64, path string) func() ([]byte, error) {
	if path == "" {
		return nil
	}
	return func() ([]byte, error) {
		// Remote mode: the encrypted file lives on the WeChat host, so ask the
		// agent there to decrypt it.
		if a.cfg.MediaURL != "" {
			return a.decryptRemote(context.Background(), path)
		}

		dst := filepath.Join(a.tmpDir, fmt.Sprintf("dec_%d_%d", msgID, a.seq.Add(1)))
		if _, err := client.DecryptImage(wcfpath.ToWine(path), wcfpath.ToWine(dst)); err != nil {
			a.log.Debug("decrypt image failed, trying raw file", "err", err)
		}
		if data, err := os.ReadFile(dst); err == nil {
			_ = os.Remove(dst)
			return data, nil
		}
		return os.ReadFile(path)
	}
}

func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

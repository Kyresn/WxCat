package onebot

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/idmap"
	"wxcat/internal/media"
)

type sentMsg struct {
	Target backend.Target
	Text   string
	Ats    []string
	Images int
	Files  []string
}

type fakeBackend struct {
	self     backend.SelfInfo
	contacts []backend.Contact
	members  map[string][]backend.Member
	sent     []sentMsg
}

func (f *fakeBackend) Name() string                    { return "fake" }
func (f *fakeBackend) Start(context.Context) error     { return nil }
func (f *fakeBackend) Stop() error                     { return nil }
func (f *fakeBackend) Events() <-chan backend.RawEvent { return make(chan backend.RawEvent) }

func (f *fakeBackend) Health(context.Context) (backend.Health, error) {
	return backend.Health{Online: true}, nil
}
func (f *fakeBackend) Self(context.Context) (backend.SelfInfo, error) { return f.self, nil }
func (f *fakeBackend) Contacts(context.Context) ([]backend.Contact, error) {
	return f.contacts, nil
}
func (f *fakeBackend) GroupMembers(_ context.Context, id string) ([]backend.Member, error) {
	return f.members[id], nil
}
func (f *fakeBackend) SendText(_ context.Context, t backend.Target, text string, ats []string) (int64, error) {
	f.sent = append(f.sent, sentMsg{Target: t, Text: text, Ats: ats})
	return int64(len(f.sent)), nil
}
func (f *fakeBackend) SendImage(_ context.Context, t backend.Target, data []byte, name string) (int64, error) {
	f.sent = append(f.sent, sentMsg{Target: t, Images: 1})
	return int64(len(f.sent)), nil
}
func (f *fakeBackend) SendFile(_ context.Context, t backend.Target, path string) (int64, error) {
	f.sent = append(f.sent, sentMsg{Target: t, Files: []string{path}})
	return int64(len(f.sent)), nil
}
func (f *fakeBackend) Recall(context.Context, int64) error { return backend.ErrUnsupported }

func newTestActions(t *testing.T, fb *fakeBackend) (*Actions, *Resolver) {
	t.Helper()
	store, err := idmap.Open(filepath.Join(t.TempDir(), "idmap.json"))
	if err != nil {
		t.Fatal(err)
	}
	ids := NewResolver(store, false)
	ids.SetSelf(fb.self.ID)

	mediaDir := t.TempDir()
	ms, err := media.New(mediaDir, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	return NewActions(cfg, fb, ids, ms, testLogger()), ids
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSendPrivateText(t *testing.T) {
	fb := &fakeBackend{self: backend.SelfInfo{ID: "wxid_bot", Name: "bot"}}
	a, ids := newTestActions(t, fb)

	uid := ids.UserID("wxid_friend")
	body := mustJSON(map[string]any{
		"user_id": uid,
		"message": []Segment{Text("hello")},
	})
	if _, aerr := a.Handle(context.Background(), "send_private_msg", body); aerr != nil {
		t.Fatalf("send failed: %v", aerr)
	}
	if len(fb.sent) != 1 || fb.sent[0].Text != "hello" || fb.sent[0].Target.Kind != backend.KindPrivate {
		t.Fatalf("unexpected send: %#v", fb.sent)
	}
}

func TestSendGroupWithAt(t *testing.T) {
	fb := &fakeBackend{self: backend.SelfInfo{ID: "wxid_bot", Name: "bot"}}
	a, ids := newTestActions(t, fb)

	body := mustJSON(map[string]any{
		"group_id": ids.GroupID("1@chatroom"),
		"message": []Segment{
			{Type: "at", Data: map[string]any{"qq": ids.UserID("wxid_member"), "name": "成员"}},
			Text(" hi"),
		},
	})
	if _, aerr := a.Handle(context.Background(), "send_group_msg", body); aerr != nil {
		t.Fatalf("send failed: %v", aerr)
	}
	if len(fb.sent) != 1 {
		t.Fatalf("expected one send, got %#v", fb.sent)
	}
	got := fb.sent[0]
	if len(got.Ats) != 1 || got.Ats[0] != "wxid_member" {
		t.Fatalf("unexpected at list: %#v", got.Ats)
	}
	if got.Text != "@成员  hi" {
		t.Fatalf("unexpected text: %q", got.Text)
	}
}

func TestSendImageDataURL(t *testing.T) {
	fb := &fakeBackend{self: backend.SelfInfo{ID: "wxid_bot"}}
	a, ids := newTestActions(t, fb)

	// 1x1 transparent PNG.
	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	body := mustJSON(map[string]any{
		"user_id": ids.UserID("wxid_friend"),
		"message": []Segment{{Type: "image", Data: map[string]any{"file": dataURL}}},
	})
	if _, aerr := a.Handle(context.Background(), "send_private_msg", body); aerr != nil {
		t.Fatalf("send failed: %v", aerr)
	}
	if len(fb.sent) != 1 || fb.sent[0].Images != 1 {
		t.Fatalf("expected one image, got %#v", fb.sent)
	}
}

func TestUnsupportedAndUnknownActions(t *testing.T) {
	fb := &fakeBackend{self: backend.SelfInfo{ID: "wxid_bot"}}
	a, _ := newTestActions(t, fb)

	if _, aerr := a.Handle(context.Background(), "delete_msg", json.RawMessage(`{"message_id":1}`)); aerr == nil || aerr.Retcode != RetUnsupported {
		t.Fatalf("delete_msg should be unsupported, got %#v", aerr)
	}
	if _, aerr := a.Handle(context.Background(), "nope", json.RawMessage(`{}`)); aerr == nil || aerr.Retcode != RetUnsupported {
		t.Fatalf("unknown action should be unsupported, got %#v", aerr)
	}
}

func TestGetLoginInfoAndLists(t *testing.T) {
	fb := &fakeBackend{
		self: backend.SelfInfo{ID: "wxid_bot", Name: "机器人"},
		contacts: []backend.Contact{
			{ID: "wxid_friend", Name: "好友"},
			{ID: "1@chatroom", Name: "测试群", IsGroup: true},
		},
	}
	a, ids := newTestActions(t, fb)

	data, aerr := a.Handle(context.Background(), "get_login_info", nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	info := data.(map[string]any)
	if info["nickname"] != "机器人" || info["user_id"] != ids.UserID("wxid_bot") {
		t.Fatalf("unexpected login info: %#v", info)
	}

	data, aerr = a.Handle(context.Background(), "get_group_list", nil)
	if aerr != nil {
		t.Fatal(aerr)
	}
	groups := data.([]map[string]any)
	if len(groups) != 1 || groups[0]["group_name"] != "测试群" {
		t.Fatalf("unexpected group list: %#v", groups)
	}
}

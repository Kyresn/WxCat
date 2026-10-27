package onebot

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/idmap"
	"wxcat/internal/media"
)

func newTestBuilder(t *testing.T) (*Builder, *Resolver) {
	t.Helper()
	store, err := idmap.Open(filepath.Join(t.TempDir(), "idmap.json"))
	if err != nil {
		t.Fatal(err)
	}
	ids := NewResolver(store, false)
	ids.SetSelf("wxid_bot")

	ms, err := media.New(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	return NewBuilder(cfg, ids, ms, testLogger()), ids
}

func TestBuilderGroupMessage(t *testing.T) {
	b, ids := newTestBuilder(t)

	ev := backend.RawEvent{
		Type:       backend.EventMessage,
		MsgID:      100,
		Time:       1700000000,
		Target:     backend.Target{Kind: backend.KindGroup, ID: "1@chatroom"},
		SenderID:   "wxid_member",
		SenderName: "成员",
		MsgKind:    backend.MsgText,
		Text:       "@机器人 你好",
		AtList:     []string{"wxid_bot"},
	}
	out := b.Message(ev)
	if out.PostType != "message" || out.MessageType != "group" || out.SubType != "normal" {
		t.Fatalf("unexpected envelope: %#v", out)
	}
	if out.GroupID != ids.GroupID("1@chatroom") {
		t.Fatalf("unexpected group id: %#v", out.GroupID)
	}
	if len(out.Segments) != 2 || out.Segments[0].Type != "at" {
		t.Fatalf("expected at + text, got %#v", out.Segments)
	}
	if out.RawMessage != SegmentsToCQ(out.Segments) {
		t.Fatal("raw_message mismatch")
	}
}

func TestBuilderImageFallsBackToDataURL(t *testing.T) {
	b, _ := newTestBuilder(t)
	png := []byte("\x89PNG\r\n\x1a\nfake")

	ev := backend.RawEvent{
		Type:     backend.EventMessage,
		MsgID:    200,
		Time:     time.Now().Unix(),
		Target:   backend.Target{Kind: backend.KindPrivate, ID: "wxid_friend"},
		SenderID: "wxid_friend",
		MsgKind:  backend.MsgImage,
		MediaLoader: func() ([]byte, error) {
			return png, nil
		},
	}
	out := b.Message(ev)
	if len(out.Segments) != 1 || out.Segments[0].Type != "image" {
		t.Fatalf("unexpected segments: %#v", out.Segments)
	}
	file, _ := out.Segments[0].Data["file"].(string)
	if !strings.HasPrefix(file, "data:image/png;base64,") {
		t.Fatalf("expected data URL, got %q", file)
	}
}

func TestBuilderNotice(t *testing.T) {
	b, ids := newTestBuilder(t)

	out := b.Notice(backend.RawEvent{
		Type:       backend.EventNotice,
		NoticeType: "group_recall",
		MsgID:      55,
		Target:     backend.Target{Kind: backend.KindGroup, ID: "1@chatroom"},
		SenderID:   "wxid_member",
		OperatorID: "wxid_member",
	})
	if out == nil || out.NoticeType != "group_recall" {
		t.Fatalf("unexpected notice: %#v", out)
	}
	if out.GroupID != ids.GroupID("1@chatroom") || out.MessageID != int64(55) {
		t.Fatalf("unexpected notice ids: %#v", out)
	}

	if got := b.Notice(backend.RawEvent{NoticeType: "unknown"}); got != nil {
		t.Fatal("unknown notice should be dropped")
	}
}

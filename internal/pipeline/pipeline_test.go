package pipeline

import (
	"testing"

	"wxcat/internal/backend"
	"wxcat/internal/config"
)

func newFilter(mode string) *Filter {
	cfg := config.Default()
	cfg.GroupMode = mode
	f := New(cfg)
	f.SetSelf("wxid_bot", "小助手")
	return f
}

func groupEvent(id int64, text string) backend.RawEvent {
	return backend.RawEvent{
		Type:   backend.EventMessage,
		MsgID:  id,
		Target: backend.Target{Kind: backend.KindGroup, ID: "1@chatroom"},
		Text:   text,
	}
}

func TestGroupAtFilter(t *testing.T) {
	f := newFilter("at")

	if f.Allow(groupEvent(1, "hello everyone")) {
		t.Fatal("unmentioned group message should be dropped")
	}
	if !f.Allow(groupEvent(2, "@小助手 你好")) {
		t.Fatal("mentioned message should pass")
	}
	if f.Allow(groupEvent(2, "@小助手 你好")) {
		t.Fatal("duplicate message should be dropped")
	}
}

func TestGroupModeAll(t *testing.T) {
	f := newFilter("all")
	if !f.Allow(groupEvent(10, "no mention")) {
		t.Fatal("groupMode=all should pass unmentioned messages")
	}
}

func TestSelfFilter(t *testing.T) {
	f := newFilter("all")
	ev := backend.RawEvent{
		Type:   backend.EventMessage,
		MsgID:  3,
		Target: backend.Target{Kind: backend.KindPrivate, ID: "wxid_friend"},
		IsSelf: true,
	}
	if f.Allow(ev) {
		t.Fatal("self message should be dropped by default")
	}
}

func TestPrivateAlwaysPasses(t *testing.T) {
	f := newFilter("at")
	ev := backend.RawEvent{
		Type:   backend.EventMessage,
		MsgID:  4,
		Target: backend.Target{Kind: backend.KindPrivate, ID: "wxid_friend"},
		Text:   "hi",
	}
	if !f.Allow(ev) {
		t.Fatal("private message should pass")
	}
}

package weflowbackend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestContactsAndMembers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/api/v1/contacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"contacts":[{"username":"wxid_friend","displayName":"好友","remark":"","nickname":"好友","type":"friend"}]}`)
	})
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"sessions":[{"username":"1@chatroom","displayName":"测试群","type":2,"unreadCount":0}]}`)
	})
	mux.HandleFunc("/api/v1/group-members", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"members":[{"wxid":"wxid_member","displayName":"成员","groupNickname":"群昵称","isOwner":true}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New(config.WeFlowConfig{BaseURL: srv.URL, SelfWxid: "wxid_bot", SendMode: "none"}, testLogger())
	ctx := context.Background()

	contacts, err := a.Contacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 2 {
		t.Fatalf("expected friend + group, got %#v", contacts)
	}
	groups := 0
	for _, c := range contacts {
		if c.IsGroup {
			groups++
			if c.Name != "测试群" {
				t.Fatalf("unexpected group name: %#v", c)
			}
		}
	}
	if groups != 1 {
		t.Fatalf("expected one group, got %d", groups)
	}

	members, err := a.GroupMembers(ctx, "1@chatroom")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ID != "wxid_member" || members[0].Role != "owner" {
		t.Fatalf("unexpected members: %#v", members)
	}

	self, err := a.Self(ctx)
	if err != nil || self.ID != "wxid_bot" {
		t.Fatalf("unexpected self: %#v %v", self, err)
	}
}

func TestSSEMessage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/api/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"messages":[
			{"localId":7,"serverId":"7001","createTime":1700000000,"isSend":0,
			 "senderUsername":"wxid_member","content":"你好","parsedContent":"你好"}
		]}`)
	})
	mux.HandleFunc("/api/v1/push/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		_, _ = io.WriteString(w, "event: message.new\n")
		_, _ = io.WriteString(w, `data: {"event":"message.new","sessionId":"1@chatroom","rawid":"7001","sourceName":"成员","content":"你好","timestamp":1700000000}`+"\n\n")
		flusher.Flush()
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New(config.WeFlowConfig{BaseURL: srv.URL, SelfWxid: "wxid_bot", SendMode: "none"}, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	select {
	case ev := <-a.Events():
		if ev.Type != backend.EventMessage || ev.MsgKind != backend.MsgText {
			t.Fatalf("unexpected event: %#v", ev)
		}
		if ev.Target.Kind != backend.KindGroup || ev.Target.ID != "1@chatroom" {
			t.Fatalf("unexpected target: %#v", ev.Target)
		}
		if ev.SenderID != "wxid_member" || ev.Text != "你好" {
			t.Fatalf("unexpected payload: %#v", ev)
		}
		if ev.MsgID != 7001 {
			t.Fatalf("unexpected msg id: %d", ev.MsgID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event from SSE")
	}
}

func TestSendHTTP(t *testing.T) {
	var got map[string]any
	sender := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer sender.Close()

	a := New(config.WeFlowConfig{
		BaseURL:   "http://127.0.0.1:1",
		SelfWxid:  "wxid_bot",
		SendMode:  "http",
		SenderURL: sender.URL,
	}, testLogger())

	if _, err := a.SendText(context.Background(), backend.Target{Kind: backend.KindGroup, ID: "1@chatroom"}, "hello", []string{"wxid_member"}); err != nil {
		t.Fatal(err)
	}
	if got["to"] != "1@chatroom" || got["text"] != "hello" {
		t.Fatalf("unexpected sender payload: %#v", got)
	}
	ats, _ := got["ats"].([]any)
	if len(ats) != 1 || ats[0] != "wxid_member" {
		t.Fatalf("unexpected ats: %#v", got["ats"])
	}
}

func TestSendUnsupportedModes(t *testing.T) {
	a := New(config.WeFlowConfig{BaseURL: "http://127.0.0.1:1", SelfWxid: "wxid_bot", SendMode: "none"}, testLogger())
	if _, err := a.SendText(context.Background(), backend.Target{Kind: backend.KindPrivate, ID: "wxid_friend"}, "hi", nil); err == nil {
		t.Fatal("expected unsupported error")
	}
	if err := a.Recall(context.Background(), 1); err == nil {
		t.Fatal("recall should be unsupported")
	}
}

func TestDeriveSelf(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"sessions":[{"username":"wxid_friend","displayName":"好友","type":1,"unreadCount":0}]}`)
	})
	mux.HandleFunc("/api/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"messages":[
			{"localId":1,"serverId":"1","createTime":1700000000,"isSend":1,"senderUsername":"wxid_self","content":"hi"}
		]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	a := New(config.WeFlowConfig{BaseURL: srv.URL, SendMode: "none"}, testLogger())
	self, err := a.Self(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if self.ID != "wxid_self" {
		t.Fatalf("unexpected derived self: %#v", self)
	}
}

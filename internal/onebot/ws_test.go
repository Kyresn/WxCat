package onebot

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"wxcat/internal/backend"
	"wxcat/internal/config"
)

func testHubAndActions(t *testing.T) (*Hub, *Actions, *fakeBackend, *Resolver) {
	t.Helper()
	fb := &fakeBackend{self: backend.SelfInfo{ID: "wxid_bot", Name: "机器人"}}
	actions, ids := newTestActions(t, fb)
	ids.SetSelf("wxid_bot")
	hub := NewHub(testLogger())
	hub.SetSelfFunc(ids.Self)
	hub.SetSelfNumFunc(ids.SelfNum)
	return hub, actions, fb, ids
}

func TestReverseWebSocketHandshake(t *testing.T) {
	hub, actions, _, _ := testHubAndActions(t)

	headers := make(chan http.Header, 1)
	lifecycle := make(chan Event, 1)
	response := make(chan ActionResponse, 1)
	serverErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()

		_, data, err := conn.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		var meta Event
		_ = json.Unmarshal(data, &meta)
		lifecycle <- meta

		req, _ := json.Marshal(ActionRequest{Action: "get_login_info", Params: json.RawMessage(`{}`), Echo: "e1"})
		if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
			serverErr <- err
			return
		}
		_, data, err = conn.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		var resp ActionResponse
		_ = json.Unmarshal(data, &resp)
		response <- resp
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	cfg := config.WebsocketClient{
		Name:              "test",
		Enable:            true,
		URL:               "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws",
		MessagePostFormat: "array",
		Token:             "secret",
		ReconnectInterval: 200,
	}
	client := NewReverseClient(cfg, hub, actions, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Start(ctx)

	select {
	case h := <-headers:
		if got := h.Get("Authorization"); got != "Token secret" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if got := h.Get("X-Client-Role"); got != "Universal" {
			t.Fatalf("unexpected role: %q", got)
		}
		if got := h.Get("X-Self-ID"); got == "" || got == "0" {
			t.Fatalf("unexpected self id: %q", got)
		}
	case err := <-serverErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
	}

	select {
	case meta := <-lifecycle:
		if meta.PostType != "meta_event" || meta.MetaEventType != "lifecycle" || meta.SubType != "connect" {
			t.Fatalf("unexpected lifecycle event: %#v", meta)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no lifecycle event")
	}

	select {
	case resp := <-response:
		if resp.Status != "ok" || resp.Echo != "e1" {
			t.Fatalf("unexpected action response: %#v", resp)
		}
		info, ok := resp.Data.(map[string]any)
		if !ok || info["nickname"] != "机器人" {
			t.Fatalf("unexpected login info: %#v", resp.Data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no action response")
	}

	cancel()
	_ = client.Stop(context.Background())
}

func TestForwardWebSocketServer(t *testing.T) {
	hub, actions, _, ids := testHubAndActions(t)

	port := freePort(t)
	cfg := config.WebsocketServer{
		Name:              "ws",
		Enable:            true,
		Host:              "127.0.0.1",
		Port:              port,
		MessagePostFormat: "array",
		Token:             "tok",
	}
	srv := NewWSServer(cfg, hub, actions, testLogger())
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop(context.Background()) }()

	header := http.Header{}
	header.Set("Authorization", "Bearer tok")
	url := "ws://127.0.0.1:" + strconv.Itoa(port) + "/"
	conn, _, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var meta Event
	_ = json.Unmarshal(data, &meta)
	if meta.PostType != "meta_event" || meta.SubType != "connect" {
		t.Fatalf("unexpected first message: %s", data)
	}

	req, _ := json.Marshal(ActionRequest{Action: "get_group_list", Params: json.RawMessage(`{}`), Echo: 7})
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatal(err)
	}
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var resp ActionResponse
	_ = json.Unmarshal(data, &resp)
	if resp.Echo != float64(7) || resp.Status != "ok" {
		t.Fatalf("unexpected response: %s", data)
	}
	_ = ids

	// Broadcast a message event and check it is rendered as an array.
	hub.Broadcast(&Event{
		Time:        time.Now().Unix(),
		SelfID:      ids.Self(),
		PostType:    "message",
		MessageType: "private",
		MessageID:   1,
		UserID:      ids.UserID("wxid_friend"),
		SubType:     "friend",
		Segments:    []Segment{Text("hi")},
		Sender:      &Sender{UserID: ids.UserID("wxid_friend"), Nickname: "好友"},
	}, false)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, data, err = conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	_ = json.Unmarshal(data, &msg)
	if msg["post_type"] != "message" {
		t.Fatalf("unexpected event: %s", data)
	}
	if _, ok := msg["message"].([]any); !ok {
		t.Fatalf("message should be an array: %s", data)
	}
}

func TestForwardWebSocketRejectsBadToken(t *testing.T) {
	hub, actions, _, _ := testHubAndActions(t)
	port := freePort(t)
	srv := NewWSServer(config.WebsocketServer{
		Name: "ws", Enable: true, Host: "127.0.0.1", Port: port,
		MessagePostFormat: "array", Token: "right",
	}, hub, actions, testLogger())
	if err := srv.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop(context.Background()) }()

	_, resp, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/", http.Header{})
	if err == nil {
		t.Fatal("expected handshake failure")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %#v", resp)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

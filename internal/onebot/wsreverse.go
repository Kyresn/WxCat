package onebot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"wxcat/internal/config"
)

// ReverseClient dials an OneBot reverse WebSocket endpoint, which is how
// AstrBot's aiocqhttp adapter expects to receive events.
//
// Writes go through a bounded queue so a stalled peer can never block the
// message pipeline.
type ReverseClient struct {
	cfg     config.WebsocketClient
	hub     *Hub
	actions *Actions
	log     *slog.Logger

	mu     sync.Mutex
	conn   *websocket.Conn
	sendCh chan []byte
	closed bool
}

// NewReverseClient builds a reverse WebSocket client.
func NewReverseClient(cfg config.WebsocketClient, hub *Hub, actions *Actions, log *slog.Logger) *ReverseClient {
	return &ReverseClient{cfg: cfg, hub: hub, actions: actions, log: log}
}

func (c *ReverseClient) Key() string      { return "reverse:" + c.cfg.Name }
func (c *ReverseClient) Format() string   { return c.cfg.MessagePostFormat }
func (c *ReverseClient) ReportSelf() bool { return c.cfg.ReportSelfMessage }

// Start launches the reconnect loop.
func (c *ReverseClient) Start(ctx context.Context) {
	go c.run(ctx)
}

// Stop closes the current connection.
func (c *ReverseClient) Stop(context.Context) error {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

func (c *ReverseClient) run(ctx context.Context) {
	base := time.Duration(c.cfg.ReconnectInterval) * time.Millisecond
	if base <= 0 {
		base = 3 * time.Second
	}
	backoff := base

	for ctx.Err() == nil {
		if c.isClosed() {
			return
		}

		header := http.Header{}
		header.Set("User-Agent", "OneBot/11 WxCat/"+Version)
		header.Set("X-Self-ID", strconv.FormatInt(c.hub.SelfNum(), 10))
		header.Set("X-Client-Role", "Universal")
		if c.cfg.Token != "" {
			header.Set("Authorization", "Token "+c.cfg.Token)
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.cfg.URL, header)
		if err != nil {
			c.log.Warn("reverse websocket dial failed", "name", c.cfg.Name, "url", c.cfg.URL, "err", err, "retry_in", backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, 30*time.Second)
			continue
		}

		backoff = base
		queue := make(chan []byte, 256)
		done := make(chan struct{})

		c.mu.Lock()
		c.conn = conn
		c.sendCh = queue
		c.mu.Unlock()

		c.hub.Add(c)
		c.send(lifecycleEvent(c.hub.SelfID(), "connect"))

		go c.writeLoop(conn, queue, done)
		c.readLoop(ctx, conn)

		close(done)
		c.hub.Remove(c.Key())

		c.mu.Lock()
		c.conn = nil
		c.sendCh = nil
		c.mu.Unlock()
		_ = conn.Close()

		if ctx.Err() != nil || c.isClosed() {
			return
		}
		c.log.Warn("reverse websocket disconnected", "name", c.cfg.Name, "retry_in", c.cfg.ReconnectInterval)
		if !sleepCtx(ctx, time.Duration(c.cfg.ReconnectInterval)*time.Millisecond) {
			return
		}
	}
}

func (c *ReverseClient) writeLoop(conn *websocket.Conn, queue <-chan []byte, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case b := <-queue:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
				// Closing the socket makes readLoop return and triggers a
				// reconnect.
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *ReverseClient) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req ActionRequest
		if err := json.Unmarshal(data, &req); err != nil || req.Action == "" {
			continue
		}
		go func(req ActionRequest) {
			res, aerr := c.actions.Handle(ctx, req.Action, req.Params)
			resp := ActionResponse{Status: "ok", Retcode: 0, Data: res, Echo: req.Echo}
			if aerr != nil {
				resp = ActionResponse{
					Status:  "failed",
					Retcode: aerr.Retcode,
					Message: aerr.Message,
					Wording: aerr.Message,
					Echo:    req.Echo,
				}
			}
			b, _ := json.Marshal(&resp)
			c.enqueue(b)
		}(req)
	}
}

// SendEvent implements Sink.
func (c *ReverseClient) SendEvent(ev *Event) error {
	b, err := renderEvent(ev, c.cfg.MessagePostFormat)
	if err != nil {
		return err
	}
	if !c.enqueue(b) {
		return fmt.Errorf("reverse websocket not connected")
	}
	return nil
}

func (c *ReverseClient) send(ev *Event) bool {
	b, err := renderEvent(ev, c.cfg.MessagePostFormat)
	if err != nil {
		return false
	}
	return c.enqueue(b)
}

func (c *ReverseClient) enqueue(b []byte) bool {
	c.mu.Lock()
	ch := c.sendCh
	c.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- b:
		return true
	default:
		c.log.Warn("reverse websocket queue full, dropping message", "name", c.cfg.Name)
		return true
	}
}

func (c *ReverseClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

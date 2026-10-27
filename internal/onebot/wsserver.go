package onebot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"wxcat/internal/config"
)

// WSServer is a forward WebSocket endpoint that OneBot clients dial into.
type WSServer struct {
	cfg     config.WebsocketServer
	hub     *Hub
	actions *Actions
	log     *slog.Logger

	upgrader websocket.Upgrader
	srv      *http.Server

	mu    sync.Mutex
	conns map[*wsConn]struct{}
}

// NewWSServer builds a forward WebSocket endpoint.
func NewWSServer(cfg config.WebsocketServer, hub *Hub, actions *Actions, log *slog.Logger) *WSServer {
	return &WSServer{
		cfg:     cfg,
		hub:     hub,
		actions: actions,
		log:     log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(*http.Request) bool { return true },
		},
		conns: make(map[*wsConn]struct{}),
	}
}

// Start begins listening.
func (s *WSServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	mux.HandleFunc("/onebot/v11/ws", s.handle)
	mux.HandleFunc("/ws", s.handle)

	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	s.srv = &http.Server{Addr: addr, Handler: mux}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("websocket server listen %s: %w", addr, err)
	}
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("websocket server stopped", "err", err)
		}
	}()
	s.log.Info("forward websocket listening", "addr", addr, "name", s.cfg.Name)
	return nil
}

// Stop closes the listener and all connections.
func (s *WSServer) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	err := s.srv.Shutdown(ctx)

	s.mu.Lock()
	conns := make([]*wsConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
	return err
}

func (s *WSServer) handle(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "invalid access token", http.StatusUnauthorized)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("websocket upgrade failed", "err", err)
		return
	}

	c := &wsConn{
		key:    fmt.Sprintf("%s/%p", s.cfg.Name, conn),
		format: s.cfg.MessagePostFormat,
		report: s.cfg.ReportSelfMessage,
		conn:   conn,
		send:   make(chan []byte, 256),
		done:   make(chan struct{}),
		log:    s.log,
	}

	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	s.hub.Add(c)
	c.sendEvent(lifecycleEvent(s.hub.SelfID(), "connect"))

	go c.writeLoop()
	go func() {
		s.readLoop(c)
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		s.hub.Remove(c.key)
		c.close()
	}()
}

func (s *WSServer) authorized(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}
	if tokenMatches(r.Header.Get("Authorization"), s.cfg.Token) {
		return true
	}
	return r.URL.Query().Get("access_token") == s.cfg.Token
}

func (s *WSServer) readLoop(c *wsConn) {
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var req ActionRequest
		if err := json.Unmarshal(data, &req); err != nil || req.Action == "" {
			continue
		}
		go func(req ActionRequest) {
			// The HTTP request context is already cancelled here, so actions
			// run under a detached context.
			data, aerr := s.actions.Handle(context.Background(), req.Action, req.Params)
			resp := ActionResponse{Status: "ok", Retcode: 0, Data: data, Echo: req.Echo}
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

type wsConn struct {
	key    string
	format string
	report bool

	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
	log  *slog.Logger
}

func (c *wsConn) Key() string      { return c.key }
func (c *wsConn) Format() string   { return c.format }
func (c *wsConn) ReportSelf() bool { return c.report }

func (c *wsConn) SendEvent(ev *Event) error {
	b, err := renderEvent(ev, c.format)
	if err != nil {
		return err
	}
	return c.enqueue(b)
}

func (c *wsConn) enqueue(b []byte) error {
	select {
	case <-c.done:
		return fmt.Errorf("connection closed")
	case c.send <- b:
		return nil
	default:
		return fmt.Errorf("send buffer full")
	}
}

func (c *wsConn) sendEvent(ev *Event) {
	_ = c.SendEvent(ev)
}

func (c *wsConn) writeLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case b := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				c.close()
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.close()
				return
			}
		}
	}
}

func (c *wsConn) close() {
	c.once.Do(func() {
		close(c.done)
		_ = c.conn.Close()
	})
}

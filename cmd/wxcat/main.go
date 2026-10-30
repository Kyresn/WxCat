// Command wxcat is a WeChat-to-OneBot 11 gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"wxcat/internal/backend"
	restbackend "wxcat/internal/backend/rest"
	wcfbackend "wxcat/internal/backend/wcf"
	weflowbackend "wxcat/internal/backend/weflow"
	"wxcat/internal/config"
	"wxcat/internal/idmap"
	"wxcat/internal/logging"
	"wxcat/internal/media"
	"wxcat/internal/onebot"
	"wxcat/internal/pipeline"
	"wxcat/internal/webui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "wxcat:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("c", envOr("WXCAT_CONFIG", "config/wxcat.json"), "path to wxcat.json")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("wxcat", onebot.Version)
		return nil
	}

	ring := logging.NewRing(500)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	cfg.ApplyEnv()

	logger := newLogger(cfg.LogLevel, ring)
	slog.SetDefault(logger)

	if _, statErr := os.Stat(*cfgPath); errors.Is(statErr, os.ErrNotExist) {
		if err := cfg.Save(*cfgPath); err != nil {
			logger.Warn("cannot write initial config", "path", *cfgPath, "err", err)
		} else {
			logger.Info("created default config", "path", *cfgPath)
		}
	}

	dataDir := envOr("WXCAT_DATA", "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	ids, err := idmap.Open(filepath.Join(dataDir, "idmap.json"))
	if err != nil {
		return err
	}
	store, err := media.New(filepath.Join(dataDir, "media"), cfg.MediaBaseURL)
	if err != nil {
		return err
	}

	resolver := onebot.NewResolver(ids, cfg.IDMode == "raw")

	be, err := newBackend(cfg, logger)
	if err != nil {
		return err
	}

	actions := onebot.NewActions(cfg, be, resolver, store, logger)
	hub := onebot.NewHub(logger)
	hub.SetSelfFunc(resolver.Self)
	hub.SetSelfNumFunc(resolver.SelfNum)
	builder := onebot.NewBuilder(cfg, resolver, store, logger)
	filter := pipeline.New(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := be.Start(ctx); err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	logger.Info("wxcat started", "version", onebot.Version, "backend", be.Name(), "id_mode", cfg.IDMode)

	var (
		httpServers []*http.Server
		wsServers   []*onebot.WSServer
		webhooks    []*onebot.WebhookClient

		reverseMu sync.Mutex
		reverses  []*onebot.ReverseClient
		reverseOn sync.Once
	)

	for _, hs := range cfg.Network.HTTPServers {
		if !hs.Enable {
			continue
		}
		api := onebot.NewHTTPAPI(actions, store, hs.Token, hs.Debug, logger)
		addr := net.JoinHostPort(hs.Host, strconv.Itoa(hs.Port))
		srv := &http.Server{Addr: addr, Handler: api.Handler()}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Error("http api listen failed", "name", hs.Name, "addr", addr, "err", err)
			continue
		}
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logger.Error("http api stopped", "addr", addr, "err", err)
			}
		}()
		httpServers = append(httpServers, srv)
		logger.Info("http api listening", "name", hs.Name, "addr", addr)
	}

	for _, ws := range cfg.Network.WebsocketServers {
		if !ws.Enable {
			continue
		}
		server := onebot.NewWSServer(ws, hub, actions, logger)
		if err := server.Start(ctx); err != nil {
			logger.Error("websocket server failed", "name", ws.Name, "err", err)
			continue
		}
		wsServers = append(wsServers, server)
	}

	for _, hc := range cfg.Network.HTTPClients {
		if !hc.Enable || hc.URL == "" {
			continue
		}
		client := onebot.NewWebhookClient(hc, logger)
		client.Start(ctx)
		hub.Add(client)
		webhooks = append(webhooks, client)
	}

	startReverses := func() {
		reverseOn.Do(func() {
			for _, rc := range cfg.Network.WebsocketClients {
				if !rc.Enable || rc.URL == "" {
					continue
				}
				client := onebot.NewReverseClient(rc, hub, actions, logger)
				client.Start(ctx)
				reverseMu.Lock()
				reverses = append(reverses, client)
				reverseMu.Unlock()
				logger.Info("reverse websocket started", "name", rc.Name, "url", rc.URL)
			}
		})
	}

	var ui *webui.Server
	if cfg.WebUI.Enable {
		ui = webui.New(cfg, *cfgPath, be, resolver, hub, actions, store, logger, ring)
		if err := ui.Start(ctx); err != nil {
			logger.Error("webui failed to start", "err", err)
		}
	}

	// Wait for the WeChat account to log in, then publish its identity and
	// start reverse WebSocket clients (they need a real self_id).
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		attempts := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				attempts++
				probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				self, err := be.Self(probeCtx)
				cancel()
				if err != nil || self.ID == "" {
					if attempts == 30 {
						// Keep the gateway usable even when the backend cannot
						// report the account (for example a REST bridge without
						// selfWxid configured).
						logger.Warn("account still unknown after 60s; starting reverse websockets with self_id=0")
						startReverses()
					}
					continue
				}
				resolver.SetSelf(self.ID)
				filter.SetSelf(self.ID, self.Name)
				logger.Info("onebot identity ready", "wxid", self.ID, "self_id", resolver.Self())
				startReverses()
				return
			}
		}
	}()

	// Event loop.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-be.Events():
				if !ok {
					return
				}
				if !filter.Allow(ev) {
					continue
				}
				switch ev.Type {
				case backend.EventNotice:
					if out := builder.Notice(ev); out != nil {
						hub.Broadcast(out, false)
					}
				default:
					out := builder.Message(ev)
					actions.RememberInbound(out)
					hub.Broadcast(out, ev.IsSelf)
				}
			}
		}
	}()

	// Heartbeat and lifecycle state machine.
	go heartbeatLoop(ctx, be, hub, logger)

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	for _, s := range wsServers {
		_ = s.Stop(shutdownCtx)
	}
	reverseMu.Lock()
	revs := append([]*onebot.ReverseClient(nil), reverses...)
	reverseMu.Unlock()
	for _, r := range revs {
		_ = r.Stop(shutdownCtx)
	}
	for _, s := range httpServers {
		_ = s.Shutdown(shutdownCtx)
	}
	if ui != nil {
		_ = ui.Stop(shutdownCtx)
	}
	_ = be.Stop()
	_ = ids.Save()
	logger.Info("bye")
	return nil
}

func heartbeatLoop(ctx context.Context, be backend.Backend, hub *onebot.Hub, log *slog.Logger) {
	const interval = 30 * time.Second

	hb := time.NewTicker(interval)
	defer hb.Stop()
	probe := time.NewTicker(10 * time.Second)
	defer probe.Stop()

	online := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-probe.C:
			probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			health, err := be.Health(probeCtx)
			cancel()
			now := err == nil && health.Online
			if now != online {
				online = now
				sub := "disable"
				if online {
					sub = "enable"
				}
				hub.Broadcast(onebot.LifecycleEvent(hub.SelfID(), sub), false)
				log.Info("backend state changed", "online", online)
			}
		case <-hb.C:
			hub.Broadcast(onebot.HeartbeatEvent(hub.SelfID(), int64(interval/time.Millisecond), online), false)
		}
	}
}

func newBackend(cfg *config.Config, log *slog.Logger) (backend.Backend, error) {
	switch strings.ToLower(cfg.Backend.Type) {
	case "wcf":
		return wcfbackend.New(cfg.Backend.WCF, log)
	case "mimicwx", "rest":
		return restbackend.New("mimicwx", cfg.Backend.MimicWX, log), nil
	case "webhook":
		return restbackend.New("webhook", cfg.Backend.Webhook, log), nil
	case "weflow":
		return weflowbackend.New(cfg.Backend.WeFlow, log), nil
	default:
		return nil, fmt.Errorf("unknown backend type %q (want wcf, mimicwx, webhook or weflow)", cfg.Backend.Type)
	}
}

func newLogger(level string, ring *logging.Ring) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	inner := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	return slog.New(logging.NewHandler(inner, ring))
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

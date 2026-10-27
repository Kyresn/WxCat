package onebot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"wxcat/internal/config"
)

// WebhookClient posts events to an HTTP endpoint (NapCat calls this the "HTTP
// client" or webhook).
type WebhookClient struct {
	cfg    config.HTTPClient
	log    *slog.Logger
	queue  chan []byte
	client *http.Client
}

// NewWebhookClient builds a webhook sink.
func NewWebhookClient(cfg config.HTTPClient, log *slog.Logger) *WebhookClient {
	return &WebhookClient{
		cfg:    cfg,
		log:    log,
		queue:  make(chan []byte, 512),
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (w *WebhookClient) Key() string      { return "webhook:" + w.cfg.Name }
func (w *WebhookClient) Format() string   { return w.cfg.MessagePostFormat }
func (w *WebhookClient) ReportSelf() bool { return w.cfg.ReportSelfMessage }

// Start launches the delivery worker.
func (w *WebhookClient) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case body := <-w.queue:
				w.deliver(ctx, body)
			}
		}
	}()
}

// SendEvent implements Sink. Events are dropped rather than blocking when the
// queue is full, so a slow webhook cannot stall the message pipeline.
func (w *WebhookClient) SendEvent(ev *Event) error {
	b, err := renderEvent(ev, w.cfg.MessagePostFormat)
	if err != nil {
		return err
	}
	select {
	case w.queue <- b:
	default:
		w.log.Warn("webhook queue full, dropping event", "name", w.cfg.Name)
	}
	return nil
}

func (w *WebhookClient) deliver(ctx context.Context, body []byte) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 && !sleepCtx(ctx, time.Duration(attempt)*time.Second) {
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL, bytes.NewReader(body))
		if err != nil {
			w.log.Warn("webhook request build failed", "err", err)
			return
		}
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		req.Header.Set("User-Agent", "OneBot/11 WxCat/"+Version)
		if w.cfg.Token != "" {
			req.Header.Set("Authorization", "Token "+w.cfg.Token)
		}
		if w.cfg.Secret != "" {
			req.Header.Set("X-Signature", "sha1="+sign(body, w.cfg.Secret))
		}

		resp, err := w.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 300 {
				return
			}
			lastErr = fmt.Errorf("webhook returned %s", resp.Status)
		} else {
			lastErr = err
		}
	}
	w.log.Warn("webhook delivery failed", "name", w.cfg.Name, "err", lastErr)
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

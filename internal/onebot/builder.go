package onebot

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
	"wxcat/internal/media"
	"wxcat/internal/wxmsg"
)

// Builder turns backend events into OneBot 11 events.
type Builder struct {
	cfg   *config.Config
	ids   *Resolver
	media *media.Store
	log   *slog.Logger
}

// NewBuilder wires a builder.
func NewBuilder(cfg *config.Config, ids *Resolver, store *media.Store, log *slog.Logger) *Builder {
	return &Builder{cfg: cfg, ids: ids, media: store, log: log}
}

// Message builds a message event.
func (b *Builder) Message(ev backend.RawEvent) *Event {
	segs := b.segments(ev)
	ts := ev.Time
	if ts == 0 {
		ts = time.Now().Unix()
	}

	e := &Event{
		Time:        ts,
		SelfID:      b.ids.Self(),
		PostType:    "message",
		MessageType: string(ev.Target.Kind),
		MessageID:   ev.MsgID,
		UserID:      b.ids.UserID(ev.SenderID),
		Font:        0,
		RawMessage:  SegmentsToCQ(segs),
		Segments:    segs,
		Sender: &Sender{
			UserID:   b.ids.UserID(ev.SenderID),
			Nickname: ev.SenderName,
		},
	}

	if ev.Target.Kind == backend.KindGroup {
		e.GroupID = b.ids.GroupID(ev.Target.ID)
		e.SubType = "normal"
		e.Sender.Card = ev.SenderName
		e.Sender.Role = "member"
	} else {
		e.SubType = "friend"
	}
	return e
}

// Notice builds a notice event. It returns nil when the raw event is not a
// notice WxCat can express.
func (b *Builder) Notice(ev backend.RawEvent) *Event {
	ts := ev.Time
	if ts == 0 {
		ts = time.Now().Unix()
	}
	e := &Event{
		Time:       ts,
		SelfID:     b.ids.Self(),
		PostType:   "notice",
		NoticeType: ev.NoticeType,
		SubType:    ev.NoticeSubType,
	}

	switch ev.NoticeType {
	case "group_recall":
		e.GroupID = b.ids.GroupID(ev.Target.ID)
		e.UserID = b.ids.UserID(ev.SenderID)
		e.OperatorID = b.ids.UserID(ev.OperatorID)
		e.MessageID = ev.MsgID
	case "friend_recall":
		e.UserID = b.ids.UserID(ev.SenderID)
		e.MessageID = ev.MsgID
	case "group_increase", "group_decrease":
		e.GroupID = b.ids.GroupID(ev.Target.ID)
		e.UserID = b.ids.UserID(ev.SenderID)
		e.OperatorID = b.ids.UserID(ev.OperatorID)
	default:
		return nil
	}
	return e
}

func (b *Builder) segments(ev backend.RawEvent) []Segment {
	var segs []Segment

	if ev.Target.Kind == backend.KindGroup {
		for _, at := range ev.AtList {
			segs = append(segs, Segment{Type: "at", Data: map[string]any{
				"qq": b.ids.UserID(at),
			}})
		}
	}

	switch ev.MsgKind {
	case backend.MsgText:
		text := strings.TrimSpace(ev.Text)
		if text == "" {
			text = " "
		}
		segs = append(segs, Text(text))
	case backend.MsgImage:
		data := map[string]any{"summary": "[图片]", "sub_type": 1}
		if f := b.mediaRef(ev, "image"); f != "" {
			data["file"] = f
			data["url"] = f
		}
		segs = append(segs, Segment{Type: "image", Data: data})
	case backend.MsgVoice:
		data := map[string]any{}
		if f := b.mediaRef(ev, "voice"); f != "" {
			data["file"] = f
		}
		segs = append(segs, Segment{Type: "record", Data: data})
	case backend.MsgVideo:
		data := map[string]any{}
		if f := b.mediaRef(ev, "video"); f != "" {
			data["file"] = f
		}
		segs = append(segs, Segment{Type: "video", Data: data})
	case backend.MsgFace:
		segs = append(segs, Text("[表情]"))
	case backend.MsgFile:
		data := map[string]any{"name": ev.MediaName}
		if f := b.mediaRef(ev, "file"); f != "" {
			data["file"] = f
			data["url"] = f
		}
		if ev.MediaName != "" {
			data["file_name"] = ev.MediaName
		}
		segs = append(segs, Segment{Type: "file", Data: data})
	case backend.MsgLink:
		title, url := "", ""
		if app, ok := wxmsg.ParseApp(ev.XML); ok {
			title, url = app.Link()
		}
		text := strings.TrimSpace(title + " " + url)
		if text == "" {
			text = "[链接]"
		}
		segs = append(segs, Text(text))
	case backend.MsgSystem:
		// System messages are reported as notices, not message events.
		return nil
	default:
		segs = append(segs, Text("[暂不支持的消息类型]"))
	}

	if len(segs) == 0 {
		segs = append(segs, Text("[暂不支持的消息类型]"))
	}
	return segs
}

// mediaRef stores the payload in the media cache and returns a URL, or a data
// URL when no public base URL is configured.
func (b *Builder) mediaRef(ev backend.RawEvent, kind string) string {
	if ev.MediaLoader == nil && ev.MediaPath == "" {
		return ""
	}
	if !b.cfg.EnableLocalFile2Url {
		return ""
	}

	data, err := loadMedia(ev)
	if err != nil {
		b.log.Warn("load media failed", "kind", kind, "err", err)
		return ""
	}
	if len(data) == 0 {
		return ""
	}

	name := ev.MediaName
	if name == "" {
		name = kind
	}
	key := fmt.Sprintf("%s:%d", kind, ev.MsgID)
	url, err := b.media.Put(key, data, name)
	if err != nil {
		b.log.Warn("cache media failed", "err", err)
		return ""
	}
	if b.media.BaseURL() == "" {
		return media.DataURL(data, name)
	}
	return url
}

func loadMedia(ev backend.RawEvent) ([]byte, error) {
	if ev.MediaLoader != nil {
		return ev.MediaLoader()
	}
	return nil, fmt.Errorf("no media loader for %s", ev.MediaPath)
}

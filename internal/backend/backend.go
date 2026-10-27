// Package backend defines the WeChat-facing side of WxCat.
//
// Every WeChat implementation (WeChatFerry over Wine, a REST bridge such as
// MimicWX, or a generic webhook adapter) is reduced to the Backend interface.
// The OneBot layer never talks to WeChat directly: it only consumes RawEvent
// values and calls the send methods below.
package backend

import (
	"context"
	"errors"
	"time"
)

// Kind tells whether a target is a private chat or a group chat.
type Kind string

const (
	KindPrivate Kind = "private"
	KindGroup   Kind = "group"
)

// MsgKind is the transport-neutral classification of a message body.
type MsgKind string

const (
	MsgText   MsgKind = "text"
	MsgImage  MsgKind = "image"
	MsgVoice  MsgKind = "voice"
	MsgVideo  MsgKind = "video"
	MsgFace   MsgKind = "face"
	MsgFile   MsgKind = "file"
	MsgLink   MsgKind = "link"
	MsgSystem MsgKind = "system"
	MsgOther  MsgKind = "other"
)

// EventType distinguishes chat messages from notices.
type EventType string

const (
	EventMessage EventType = "message"
	EventNotice  EventType = "notice"
)

// Target is a chat address in WeChat terms: a wxid for private chats, or a
// room id such as 12345678@chatroom for groups.
type Target struct {
	Kind Kind
	ID   string
}

// RawEvent is the backend-neutral form of one incoming WeChat event.
type RawEvent struct {
	Type EventType

	// MsgID is the platform message id. It must be stable for deduplication.
	MsgID int64
	Time  int64

	Target     Target
	TargetName string

	SenderID   string
	SenderName string
	IsSelf     bool

	MsgKind MsgKind
	Text    string
	XML     string

	// MediaPath is the backend-local path of an encrypted or plain media file.
	MediaPath string
	// MediaLoader lazily returns the plaintext bytes of the media file. It may
	// be nil when the backend cannot decrypt or fetch the payload.
	MediaLoader func() ([]byte, error)
	// MediaName is the original file name, when known.
	MediaName string

	AtList []string

	NoticeType    string
	NoticeSubType string
	OperatorID    string

	Raw any
}

// Contact is a friend or a group.
type Contact struct {
	ID          string
	Name        string
	Remark      string
	IsGroup     bool
	MemberCount int
}

// Member is one group member.
type Member struct {
	ID   string
	Name string
	Card string
	Role string
}

// SelfInfo describes the logged-in account.
type SelfInfo struct {
	ID   string
	Name string
}

// Health is the result of a backend liveness probe.
type Health struct {
	Online bool
	Detail string
	Since  time.Time
}

// Backend is the only surface the OneBot layer depends on.
type Backend interface {
	Name() string

	// Start launches the backend. It must return promptly; long-running
	// reconnect loops belong in the implementation's own goroutines.
	Start(ctx context.Context) error
	Stop() error

	Health(ctx context.Context) (Health, error)
	Self(ctx context.Context) (SelfInfo, error)

	Contacts(ctx context.Context) ([]Contact, error)
	GroupMembers(ctx context.Context, groupID string) ([]Member, error)

	SendText(ctx context.Context, t Target, text string, ats []string) (int64, error)
	SendImage(ctx context.Context, t Target, data []byte, name string) (int64, error)
	SendFile(ctx context.Context, t Target, path string) (int64, error)

	// Recall removes a message. Backends without a recall primitive return
	// ErrUnsupported.
	Recall(ctx context.Context, msgID int64) error

	Events() <-chan RawEvent
}

var (
	// ErrUnsupported means the WeChat backend has no primitive for the action.
	ErrUnsupported = errors.New("operation not supported by this backend")
	// ErrNotLogin means the WeChat account is not logged in yet.
	ErrNotLogin = errors.New("wechat account is not logged in")
	// ErrNotFound means the contact, group or message does not exist.
	ErrNotFound = errors.New("target not found")
)

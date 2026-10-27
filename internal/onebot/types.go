// Package onebot implements the OneBot 11 wire protocol: event shaping, the
// four transports (HTTP API, forward WebSocket, reverse WebSocket, HTTP
// webhook) and the action router.
package onebot

import "encoding/json"

// Sender mirrors the OneBot 11 sender object.
type Sender struct {
	UserID   any    `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card,omitempty"`
	Role     string `json:"role,omitempty"`
}

// Status is the heartbeat status object.
type Status struct {
	Online bool `json:"online"`
	Good   bool `json:"good"`
}

// Event is a superset of the OneBot 11 event types. Unused fields stay empty
// and are omitted from JSON.
type Event struct {
	Time     int64  `json:"time"`
	SelfID   any    `json:"self_id"`
	PostType string `json:"post_type"`

	// message events
	MessageType string  `json:"message_type,omitempty"`
	SubType     string  `json:"sub_type,omitempty"`
	MessageID   any     `json:"message_id,omitempty"`
	UserID      any     `json:"user_id,omitempty"`
	GroupID     any     `json:"group_id,omitempty"`
	Message     any     `json:"message,omitempty"`
	RawMessage  string  `json:"raw_message,omitempty"`
	Font        int     `json:"font,omitempty"`
	Sender      *Sender `json:"sender,omitempty"`

	// notice events
	NoticeType string `json:"notice_type,omitempty"`
	OperatorID any    `json:"operator_id,omitempty"`
	TargetID   any    `json:"target_id,omitempty"`

	// meta events
	MetaEventType string  `json:"meta_event_type,omitempty"`
	Status        *Status `json:"status,omitempty"`
	Interval      int64   `json:"interval,omitempty"`

	// Segments is the structured form of Message. Sinks render it as an array
	// or as a CQ string depending on their configured messagePostFormat.
	Segments []Segment `json:"-"`
}

// Segment is one OneBot 11 message segment.
type Segment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// ActionRequest is what a OneBot client sends over HTTP or WebSocket.
type ActionRequest struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Echo   any             `json:"echo,omitempty"`
}

// ActionResponse is the standard OneBot 11 reply.
type ActionResponse struct {
	Status  string `json:"status"`
	Retcode int    `json:"retcode"`
	Data    any    `json:"data"`
	Message string `json:"message,omitempty"`
	Wording string `json:"wording,omitempty"`
	Echo    any    `json:"echo,omitempty"`
}

// OneBot 11 return codes used by WxCat.
const (
	RetOK            = 0
	RetBadRequest    = 100
	RetBadParam      = 102
	RetNoSession     = 103
	RetNoPermission  = 104
	RetNotFound      = 1404
	RetUnsupported   = 1404
	RetInternalError = 1500
)

// ActionError carries a OneBot return code and message.
type ActionError struct {
	Retcode int
	Message string
}

func (e *ActionError) Error() string { return e.Message }

func errBad(msg string) *ActionError      { return &ActionError{Retcode: RetBadParam, Message: msg} }
func errNotFound(msg string) *ActionError { return &ActionError{Retcode: RetNotFound, Message: msg} }
func errUnsupported(msg string) *ActionError {
	return &ActionError{Retcode: RetUnsupported, Message: msg}
}
func errInternal(msg string) *ActionError {
	return &ActionError{Retcode: RetInternalError, Message: msg}
}

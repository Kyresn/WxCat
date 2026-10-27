// Package wxmsg parses the XML payloads WeChat embeds in app messages and
// system messages.
package wxmsg

import (
	"encoding/xml"
	"strings"
)

// AppMessage is the subset of the app-message XML WxCat understands.
type AppMessage struct {
	XMLName xml.Name `xml:"msg"`
	AppMsg  struct {
		Title  string `xml:"title"`
		Des    string `xml:"des"`
		Type   int    `xml:"type"`
		URL    string `xml:"url"`
		AppID  string `xml:"appid"`
		Source struct {
			Name string `xml:"name"`
		} `xml:"sourcedisplayname"`
		AppAttach struct {
			TotalLen int64  `xml:"totallen"`
			FileExt  string `xml:"fileext"`
		} `xml:"appattach"`
	} `xml:"appmsg"`
}

// AppType values used by WeChat.
const (
	AppTypeText     = 1
	AppTypeImage    = 2
	AppTypeLink     = 5
	AppTypeFile     = 6
	AppTypeEmoji    = 8
	AppTypeMiniProg = 33
	AppTypeQuote    = 57
	AppTypeForward  = 19
)

// ParseApp decodes an app message payload.
func ParseApp(raw string) (AppMessage, bool) {
	var m AppMessage
	if raw == "" || !strings.Contains(raw, "<appmsg") {
		return m, false
	}
	if err := xml.Unmarshal([]byte(raw), &m); err != nil {
		return m, false
	}
	return m, true
}

// Link returns the title and URL of a link message.
func (m AppMessage) Link() (string, string) {
	return strings.TrimSpace(m.AppMsg.Title), strings.TrimSpace(m.AppMsg.URL)
}

// FileName returns the display name of a file message.
func (m AppMessage) FileName() string {
	name := strings.TrimSpace(m.AppMsg.Title)
	return name
}

// RevokeMessage is the recall system message payload.
type RevokeMessage struct {
	XMLName   xml.Name `xml:"sysmsg"`
	Type      string   `xml:"type,attr"`
	RevokeMsg struct {
		Session    string `xml:"session"`
		NewMsgID   int64  `xml:"newmsgid"`
		ReplaceMsg string `xml:"replacemsg"`
		ExMsg      string `xml:"exmsg"`
	} `xml:"revokemsg"`
}

// ParseRevoke extracts a recall from a system message.
func ParseRevoke(raw string) (msgID int64, ok bool) {
	if raw == "" || !strings.Contains(raw, "revokemsg") {
		return 0, false
	}
	var m RevokeMessage
	if err := xml.Unmarshal([]byte(raw), &m); err != nil {
		return 0, false
	}
	return m.RevokeMsg.NewMsgID, m.RevokeMsg.NewMsgID != 0
}

// SystemMessage is the generic system-message envelope.
type SystemMessage struct {
	XMLName xml.Name `xml:"sysmsg"`
	Type    string   `xml:"type,attr"`
}

// ParseSystemType returns the sysmsg type attribute (for example "revokemsg"
// or "sysmsgtemplate").
func ParseSystemType(raw string) string {
	if raw == "" || !strings.Contains(raw, "<sysmsg") {
		return ""
	}
	var m SystemMessage
	if err := xml.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	return m.Type
}

// StripGroupPrefix removes the "wxid:\n" prefix WeChat puts in front of group
// message bodies.
func StripGroupPrefix(content, sender string) string {
	if sender == "" {
		return content
	}
	for _, sep := range []string{":\n", ":\r\n"} {
		prefix := sender + sep
		if strings.HasPrefix(content, prefix) {
			return content[len(prefix):]
		}
	}
	return content
}

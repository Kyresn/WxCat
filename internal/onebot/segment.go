package onebot

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CQ code escaping per the OneBot 11 specification.
var cqEscaper = strings.NewReplacer(
	"&", "&amp;",
	"[", "&#91;",
	"]", "&#93;",
)

var cqUnescaper = strings.NewReplacer(
	"&#91;", "[",
	"&#93;", "]",
	"&amp;", "&",
)

var cqValueEscaper = strings.NewReplacer(
	"&", "&amp;",
	"[", "&#91;",
	"]", "&#93;",
	",", "&#44;",
)

var cqValueUnescaper = strings.NewReplacer(
	"&#44;", ",",
	"&#91;", "[",
	"&#93;", "]",
	"&amp;", "&",
)

// Text builds a text segment.
func Text(s string) Segment {
	return Segment{Type: "text", Data: map[string]any{"text": s}}
}

// SegmentsToCQ renders segments as a CQ-code string.
func SegmentsToCQ(segs []Segment) string {
	var b strings.Builder
	for _, seg := range segs {
		if seg.Type == "text" {
			b.WriteString(cqEscaper.Replace(stringVal(seg.Data["text"])))
			continue
		}
		b.WriteString("[CQ:")
		b.WriteString(seg.Type)
		keys := make([]string, 0, len(seg.Data))
		for k := range seg.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(",")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(cqValueEscaper.Replace(stringVal(seg.Data[k])))
		}
		b.WriteString("]")
	}
	return b.String()
}

// ParseCQ parses a CQ-code string into segments.
func ParseCQ(s string) ([]Segment, error) {
	var segs []Segment
	var text strings.Builder

	flush := func() {
		if text.Len() > 0 {
			segs = append(segs, Text(cqUnescaper.Replace(text.String())))
			text.Reset()
		}
	}

	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], "[CQ:") {
			end := strings.IndexByte(s[i:], ']')
			if end < 0 {
				text.WriteString(s[i:])
				break
			}
			body := s[i+4 : i+end]
			flush()
			seg, err := parseCQBody(body)
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg)
			i += end + 1
			continue
		}
		text.WriteByte(s[i])
		i++
	}
	flush()
	return segs, nil
}

func parseCQBody(body string) (Segment, error) {
	parts := strings.Split(body, ",")
	if len(parts) == 0 || parts[0] == "" {
		return Segment{}, fmt.Errorf("empty CQ code")
	}
	seg := Segment{Type: parts[0], Data: map[string]any{}}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		seg.Data[k] = cqValueUnescaper.Replace(v)
	}
	return seg, nil
}

// ParseMessage accepts either an array of segments or a CQ string, matching
// the messagePostFormat of the sending client.
func ParseMessage(raw json.RawMessage) ([]Segment, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return ParseCQ(s)
	}
	var segs []Segment
	if err := json.Unmarshal(raw, &segs); err != nil {
		return nil, fmt.Errorf("message must be a string or an array of segments: %w", err)
	}
	for i := range segs {
		if segs[i].Data == nil {
			segs[i].Data = map[string]any{}
		}
	}
	return segs, nil
}

// RenderMessage returns the value placed in the "message" field for the given
// post format.
func RenderMessage(segs []Segment, format string) any {
	if format == "string" {
		return SegmentsToCQ(segs)
	}
	if segs == nil {
		return []Segment{}
	}
	return segs
}

func stringVal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func bytesTrimSpace(b []byte) []byte {
	start := 0
	for start < len(b) && (b[start] == ' ' || b[start] == '\n' || b[start] == '\r' || b[start] == '\t') {
		start++
	}
	end := len(b)
	for end > start && (b[end-1] == ' ' || b[end-1] == '\n' || b[end-1] == '\r' || b[end-1] == '\t') {
		end--
	}
	return b[start:end]
}

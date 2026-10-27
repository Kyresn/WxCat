package onebot

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSegmentsCQRoundTrip(t *testing.T) {
	segs := []Segment{
		Text("hello [world] & <ok>"),
		{Type: "at", Data: map[string]any{"qq": "12345"}},
		{Type: "image", Data: map[string]any{"file": "http://example.com/a.png?x=1,y=2"}},
		Text("tail"),
	}

	cq := SegmentsToCQ(segs)
	if cq == "" {
		t.Fatal("empty CQ string")
	}
	got, err := ParseCQ(cq)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !reflect.DeepEqual(got, segs) {
		t.Fatalf("round trip mismatch\nwant %#v\ngot  %#v", segs, got)
	}
}

func TestParseMessageFormats(t *testing.T) {
	arr, err := ParseMessage(json.RawMessage(`[{"type":"text","data":{"text":"hi"}}]`))
	if err != nil || len(arr) != 1 || arr[0].Data["text"] != "hi" {
		t.Fatalf("array parse failed: %v %#v", err, arr)
	}

	str, err := ParseMessage(json.RawMessage(`"hi [CQ:at,qq=42]"`))
	if err != nil || len(str) != 2 || str[1].Type != "at" {
		t.Fatalf("string parse failed: %v %#v", err, str)
	}

	if _, err := ParseMessage(json.RawMessage(`123`)); err == nil {
		t.Fatal("expected error for numeric message")
	}
}

func TestRenderMessage(t *testing.T) {
	segs := []Segment{Text("a"), {Type: "at", Data: map[string]any{"qq": "1"}}}
	if got := RenderMessage(segs, "string"); got != "a[CQ:at,qq=1]" {
		t.Fatalf("unexpected string form: %v", got)
	}
	got, ok := RenderMessage(segs, "array").([]Segment)
	if !ok || len(got) != 2 {
		t.Fatalf("unexpected array form: %#v", RenderMessage(segs, "array"))
	}
}

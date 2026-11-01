package jsonx

import (
	"testing"
)

const doc = `{
  "contents": {"twoColumn": {"tabs": [
    {"tab": {"title": "Videos", "content": {"items": [
      {"videoRenderer": {"videoId": "vid00000001", "title": {"runs": [{"text": "Hello "}, {"text": "world"}]}, "viewCount": "12,345"}},
      {"videoRenderer": {"videoId": "vid00000002", "title": {"simpleText": "Second"}, "big": 9007199254740993}}
    ]}}},
    {"tab": {"title": "Shorts"}}
  ]}},
  "flag": true,
  "attributed": {"content": "plain content"}
}`

func mustDecode(t *testing.T) any {
	t.Helper()
	v, err := Decode([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGetAndTypedAccessors(t *testing.T) {
	v := mustDecode(t)
	if got := String(v, "contents", "twoColumn", "tabs", 1, "tab", "title"); got != "Shorts" {
		t.Errorf("String = %q", got)
	}
	if Get(v, "contents", "missing", 3) != nil || Get(v, "contents", "twoColumn", "tabs", 9) != nil {
		t.Error("missing paths must return nil")
	}
	if !Bool(v, "flag") {
		t.Error("Bool")
	}
	if len(Slice(v, "contents", "twoColumn", "tabs")) != 2 || Map(v, "contents") == nil {
		t.Error("Slice/Map")
	}
}

func TestFindAllIsTypeBased(t *testing.T) {
	v := mustDecode(t)
	vids := FindMaps(v, "videoRenderer")
	if len(vids) != 2 {
		t.Fatalf("found %d videoRenderers", len(vids))
	}
	if String(vids[0], "videoId") != "vid00000001" || String(vids[1], "videoId") != "vid00000002" {
		t.Error("document order not kept")
	}
	if TextAt(vids[0], "title") != "Hello world" || TextAt(vids[1], "title") != "Second" {
		t.Error("Text runs/simpleText")
	}
	if First(v, "nope") != nil {
		t.Error("First of a missing key should be nil")
	}
	if Text(Get(v, "attributed")) != "plain content" {
		t.Error("Text content")
	}
}

func TestIntKeepsPrecisionAndParsesStrings(t *testing.T) {
	v := mustDecode(t)
	vids := FindMaps(v, "videoRenderer")
	if n, ok := Int(vids[0], "viewCount"); !ok || n != 12345 {
		t.Errorf("Int(\"12,345\") = %d %v", n, ok)
	}
	if n, ok := Int(vids[1], "big"); !ok || n != 9007199254740993 {
		t.Errorf("big int lost precision: %d", n)
	}
	if _, ok := Int(vids[1], "title"); ok {
		t.Error("Int of an object must fail")
	}
	arabicSep, _ := Decode([]byte(`{"n": "1٬234"}`))
	if n, ok := Int(arabicSep, "n"); !ok || n != 1234 {
		t.Errorf("Arabic thousands separator: %d %v", n, ok)
	}
}

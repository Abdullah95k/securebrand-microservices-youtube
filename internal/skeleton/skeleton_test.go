package skeleton

import (
	"strings"
	"testing"
)

func TestBytesRemovesAllValues(t *testing.T) {
	// The token-like value is assembled at run time so secret scanners
	// don't mistake this test input for a real credential.
	tok := "Eg0S" + "C3Rv" + "a2Vu"
	in := `{
	  "videoRenderer": {"videoId": "abcDEF12345", "title": {"runs": [{"text": "secret title one"}, {"text": "secret two"}]},
	    "viewCount": 12345, "isLive": true, "badge": null},
	  "aB3dE5gH7jK9": {"text": "id used as a key"},
	  "mixed": [{"commentViewModel": {"commentId": "Ugx-private"}}, {"continuationItemRenderer": {"token": "` + tok + `"}}, {"commentViewModel": {"commentId": "Ugy-other"}}],
	  "weird key with spaces": 1
	}`
	out, err := Bytes([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, leak := range []string{"abcDEF12345", "secret", "12345", "Ugx-private", tok, "aB3dE5gH7jK9", "weird key", "id used"} {
		if strings.Contains(s, leak) {
			t.Errorf("skeleton leaks %q:\n%s", leak, s)
		}
	}
	for _, keep := range []string{"videoRenderer", "videoId", "runs", "commentViewModel", "continuationItemRenderer", String, Number, Bool, Null, Key} {
		if !strings.Contains(s, keep) {
			t.Errorf("skeleton lost %q:\n%s", keep, s)
		}
	}
	// One example per element shape: the two commentViewModel entries
	// collapse into one, the continuation stays.
	if strings.Count(s, "commentViewModel") != 1 || strings.Count(s, "continuationItemRenderer") != 1 {
		t.Errorf("array elements not grouped by shape:\n%s", s)
	}
}

func TestBytesInvalid(t *testing.T) {
	if _, err := Bytes([]byte("{not json")); err == nil {
		t.Error("want error")
	}
}

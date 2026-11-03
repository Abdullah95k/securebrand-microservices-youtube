package metrics

import (
	"strings"
	"testing"
)

func TestTextFormat(t *testing.T) {
	r := New()
	r.Set("yt_queue_due", "Jobs due now.", map[string]string{"kind": "video", "priority": "0"}, 12)
	r.Add("yt_jobs_total", "Jobs run.", map[string]string{"kind": "video", "result": "ok"}, 1)
	r.Add("yt_jobs_total", "Jobs run.", map[string]string{"result": "ok", "kind": "video"}, 2)
	r.Set("yt_label_escape", "x", map[string]string{"v": `a"b`}, 1)
	var b strings.Builder
	_, _ = r.WriteTo(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE yt_queue_due gauge",
		`yt_queue_due{kind="video",priority="0"} 12`,
		"# TYPE yt_jobs_total counter",
		`yt_jobs_total{kind="video",result="ok"} 3`,
		`yt_label_escape{v="a\"b"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	r.Reset("yt_queue_due")
	b.Reset()
	_, _ = r.WriteTo(&b)
	if strings.Contains(b.String(), `kind="video",priority="0"`) {
		t.Error("reset must drop old samples")
	}
}

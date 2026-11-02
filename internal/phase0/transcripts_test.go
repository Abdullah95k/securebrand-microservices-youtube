package phase0

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTranscriptsGoNoGo(t *testing.T) {
	yt, env := paceEnv(t)
	tracks := map[string]string{
		"fakeTRa": "https://www.youtube.com/api/timedtext?v=fakeTRa&lang=ar",       // answers with text
		"fakeTRb": "https://www.youtube.com/api/timedtext_empty?v=fakeTRb&lang=ar", // empty without a token
	}
	yt.Handle("player", func(body map[string]any) (int, any) {
		id, _ := body["videoId"].(string)
		resp := map[string]any{"playabilityStatus": map[string]any{"status": "OK"}, "videoDetails": map[string]any{"videoId": id}}
		if u, ok := tracks[id]; ok {
			resp["captions"] = map[string]any{"playerCaptionsTracklistRenderer": map[string]any{"captionTracks": []any{
				map[string]any{"baseUrl": u, "languageCode": "ar", "kind": "asr"},
			}}}
		}
		return 200, resp
	})
	yt.SetPage("/api/timedtext", `{"events":[{"segs":[{"utf8":"a"}]},{"segs":[{"utf8":"b"}]},{"tStartMs":5},{"segs":[{"utf8":"c"}]}]}`)
	yt.SetPage("/api/timedtext_empty", "")
	yt.Handle("next", func(body map[string]any) (int, any) {
		id, _ := body["videoId"].(string)
		w := map[string]any{"currentVideoEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": id}}}
		if id == "fakeTRa" {
			w["engagementPanels"] = []any{map[string]any{"engagementPanelSectionListRenderer": map[string]any{
				"panelIdentifier": "engagement-panel-searchable-transcript",
				"content": map[string]any{"continuationItemRenderer": map[string]any{"continuationEndpoint": map[string]any{
					"getTranscriptEndpoint": map[string]any{"params": "TRPARAMS_A"}}}}}}}
		}
		return 200, w
	})
	yt.Handle("get_transcript", func(body map[string]any) (int, any) {
		if body["params"] != "TRPARAMS_A" {
			return 400, map[string]any{"error": map[string]any{"code": 400}}
		}
		seg := map[string]any{"transcriptSegmentRenderer": map[string]any{"snippet": map[string]any{"runs": []any{map[string]any{"text": "zqtranscriptline"}}}}}
		return 200, map[string]any{"actions": []any{map[string]any{"updateEngagementPanelAction": map[string]any{"content": map[string]any{
			"transcriptRenderer": map[string]any{"body": map[string]any{"transcriptSegmentListRenderer": map[string]any{"initialSegments": []any{seg, seg}}}}}}}}}
	})
	if err := Transcripts(context.Background(), env.Collector, []string{"fakeTRa", "fakeTRb", "fakeTRc"}, quietLog); err != nil {
		t.Fatal(err)
	}
	recs, _ := ReadAll[TranscriptRecord](env.Store, "transcripts")
	if len(recs) != 3 {
		t.Fatalf("records %+v", recs)
	}
	a, b, c := recs[0], recs[1], recs[2]
	if a.Tracks != 1 || !a.AutoOnly || a.TimedtextCues != 3 || !a.PanelOffered || a.GetSegments != 2 || a.GetErr != "" {
		t.Errorf("video a %+v; want 3 caption cues and 2 transcript lines", a)
	}
	if b.Tracks != 1 || b.TimedtextBytes != 0 || b.TimedtextCues != 0 || b.TimedtextErr != "" || b.PanelOffered {
		t.Errorf("video b %+v; want an empty caption answer and no transcript panel", b)
	}
	if c.Tracks != 0 || c.PanelOffered {
		t.Errorf("video c %+v; want no captions at all", c)
	}
	// The caption request never carries a token parameter.
	for _, r := range yt.Requests() {
		if strings.Contains(r.Path, "timedtext") && strings.Contains(r.Path, "pot=") {
			t.Errorf("caption request sent a token: %s", r.Path)
		}
	}
	// No transcript text is stored.
	data, _ := os.ReadFile(filepath.Join(env.Store.Dir(), "transcripts.jsonl"))
	if strings.Contains(string(data), "zqtranscriptline") {
		t.Error("transcript text must not be stored")
	}

	var rep strings.Builder
	if err := Report(env.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := rep.String()
	for _, want := range []string{
		"## Transcripts without anti-bot tokens (E12)",
		"| Videos checked / player answered / with caption files | 3 / 3 / 2 |",
		"| Caption files that returned text without a token | 50% (1 of 2) |",
		"| ... that returned transcript lines | 100% (1 of 1) |",
		"keep transcripts (go)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report misses %q:\n%s", want, out)
		}
	}
}

func TestTranscriptsNoGoVerdict(t *testing.T) {
	_, env := paceEnv(t)
	_ = env.Store.Append("transcripts", TranscriptRecord{VideoHash: "x", Tracks: 1})
	_ = env.Store.Append("transcripts", TranscriptRecord{VideoHash: "y", PanelOffered: true, GetErr: "innertube: HTTP 400"})
	var rep strings.Builder
	if err := Report(env.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.String(), "drop transcripts (no-go)") {
		t.Errorf("report:\n%s", rep.String())
	}
}

func TestTranscriptsInconclusiveWhenNothingTried(t *testing.T) {
	_, env := paceEnv(t)
	_ = env.Store.Append("transcripts", TranscriptRecord{VideoHash: "x", PlayerErr: "player endpoint resting after a block; skipped"})
	_ = env.Store.Append("transcripts", TranscriptRecord{VideoHash: "y", PlayerErr: "player endpoint resting after a block; skipped"})
	var rep strings.Builder
	if err := Report(env.Store, &rep, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.String(), "Verdict: inconclusive") || strings.Contains(rep.String(), "no-go") {
		t.Errorf("report:\n%s", rep.String())
	}
}

// TestSlowPlayerIsSkippedNotRested: a player answer slower than playerWait
// skips that call only; the next call tries again (no 30-minute rest).
func TestSlowPlayerIsSkippedNotRested(t *testing.T) {
	yt, env := paceEnv(t)
	old := playerWait
	playerWait = 100 * time.Millisecond
	defer func() { playerWait = old }()
	var calls atomic.Int32
	yt.Handle("player", func(map[string]any) (int, any) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		return 200, map[string]any{"playabilityStatus": map[string]any{"status": "OK"}}
	})
	var cost Cost
	for i := 0; i < 2; i++ {
		if _, err := env.Collector.playerBestEffort(context.Background(), &cost, "player", "fakeV1", ""); err == nil {
			t.Fatal("want a timeout")
		}
	}
	if cost.ByEndpoint["player:skipped"] != 0 || calls.Load() < 2 {
		t.Errorf("cost %v, %d player calls; a slow answer must not rest the player", cost.ByEndpoint, calls.Load())
	}
}

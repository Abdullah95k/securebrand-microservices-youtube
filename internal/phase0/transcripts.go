package phase0

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// Transcripts within the rules (E12): for each video, two ways are tried
// without any anti-bot token (we never generate one, plan §3):
//
//  1. the caption file the player response links to (timedtext), and
//  2. the watch page's "show transcript" request (get_transcript), when the
//     page offers one.
//
// If neither returns text, transcripts are dropped from the plan. Records
// keep counts and statuses only, never the transcript text.

// TranscriptRecord is one video (no IDs or text).
type TranscriptRecord struct {
	At              time.Time `json:"at"`
	VideoHash       string    `json:"video_hash"`
	PlayerErr       string    `json:"player_err,omitempty"`
	Tracks          int       `json:"tracks"`
	AutoOnly        bool      `json:"auto_only"` // only automatic captions
	TimedtextStatus int       `json:"timedtext_status"`
	TimedtextBytes  int       `json:"timedtext_bytes"`
	TimedtextCues   int       `json:"timedtext_cues"`
	TimedtextErr    string    `json:"timedtext_err,omitempty"`
	PanelOffered    bool      `json:"panel_offered"`
	GetStatus       int       `json:"get_status"`
	GetSegments     int       `json:"get_segments"`
	GetErr          string    `json:"get_err,omitempty"`
}

// Transcripts checks each video and appends one record per video.
func Transcripts(ctx context.Context, col *Collector, videos []string, log *slog.Logger) error {
	if len(videos) == 0 {
		return errors.New("transcripts: no videos to check")
	}
	for i, id := range videos {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rec := col.checkTranscript(ctx, id)
		if err := col.Store.Append("transcripts", rec); err != nil {
			return err
		}
		log.Info("transcript check", "n", i+1, "of", len(videos), "tracks", rec.Tracks, "caption_cues", rec.TimedtextCues,
			"panel", rec.PanelOffered, "transcript_lines", rec.GetSegments)
	}
	return nil
}

func (c *Collector) checkTranscript(ctx context.Context, id string) TranscriptRecord {
	rec := TranscriptRecord{At: c.Now(), VideoHash: shortHash(id)}
	var cost Cost
	// 1. The caption file the player links to.
	if pr, err := c.playerBestEffort(ctx, &cost, "player:captions", id, ""); err != nil {
		rec.PlayerErr = err.Error()
	} else {
		tracks := ytparse.ParsePlayer(pr.Data).CaptionTracks
		rec.Tracks = len(tracks)
		if len(tracks) > 0 {
			t := pickTrack(tracks)
			rec.AutoOnly = true
			for _, tr := range tracks {
				rec.AutoOnly = rec.AutoOnly && tr.Auto
			}
			path := youtubePath(t.URL)
			if path == "" {
				rec.TimedtextErr = "caption URL is not on youtube.com"
			} else {
				if !strings.Contains(path, "fmt=") {
					path += "&fmt=json3"
				}
				r, err := c.Client.Get(ctx, proxypool.ClassWatch, path, nil)
				cost.add("timedtext", r)
				if r != nil {
					rec.TimedtextStatus, rec.TimedtextBytes = r.Status, len(r.Body)
					rec.TimedtextCues = ytparse.CaptionCues(r.Body)
				}
				if err != nil {
					rec.TimedtextErr = err.Error()
				}
			}
		}
	}
	// 2. The watch page's "show transcript" request.
	wr, err := c.call(ctx, &cost, innertube.Next, "next:watch", map[string]any{"videoId": id})
	if err != nil {
		rec.GetErr = "watch page: " + err.Error()
		return rec
	}
	params := ytparse.TranscriptParams(wr.Data)
	rec.PanelOffered = params != ""
	if !rec.PanelOffered {
		return rec
	}
	tr, err := c.callVia(ctx, &cost, innertube.GetTranscript, "get_transcript", map[string]any{"params": params}, wr.Proxy)
	if tr != nil {
		rec.GetStatus = tr.Status
		rec.GetSegments = ytparse.TranscriptSegments(tr.Data)
	}
	if err != nil {
		rec.GetErr = err.Error()
	}
	return rec
}

// pickTrack prefers Arabic, then a non-automatic track, then the first.
func pickTrack(tracks []ytparse.CaptionTrack) ytparse.CaptionTrack {
	for _, t := range tracks {
		if strings.HasPrefix(t.Lang, "ar") {
			return t
		}
	}
	for _, t := range tracks {
		if !t.Auto {
			return t
		}
	}
	return tracks[0]
}

// reportTranscripts writes E12 and its go/no-go.
func reportTranscripts(p func(string, ...any), recs []TranscriptRecord) {
	if len(recs) == 0 {
		return
	}
	playerOK, withTracks, captionText, captionEmpty, captionFailed, offered, transcriptText := 0, 0, 0, 0, 0, 0, 0
	anyText := 0
	for _, r := range recs {
		if r.PlayerErr == "" {
			playerOK++
		}
		if r.Tracks > 0 {
			withTracks++
			switch {
			case r.TimedtextCues > 0:
				captionText++
			case r.TimedtextErr != "":
				captionFailed++
			default:
				captionEmpty++
			}
		}
		if r.PanelOffered {
			offered++
			if r.GetSegments > 0 {
				transcriptText++
			}
		}
		if r.TimedtextCues > 0 || r.GetSegments > 0 {
			anyText++
		}
	}
	p("## Transcripts without anti-bot tokens (E12)")
	p("")
	p("| | Value |")
	p("|---|---|")
	p("| Videos checked / player answered / with caption files | %d / %d / %d |", len(recs), playerOK, withTracks)
	p("| Caption files that returned text without a token | %s (%d of %d) |", pctStr(share(captionText, withTracks)), captionText, withTracks)
	p("| ... returned an empty answer / failed | %d / %d |", captionEmpty, captionFailed)
	p("| Watch pages offering \"show transcript\" | %s (%d of %d) |", pctStr(share(offered, len(recs))), offered, len(recs))
	p("| ... that returned transcript lines | %s (%d of %d) |", pctStr(share(transcriptText, offered)), transcriptText, offered)
	p("")
	switch {
	case anyText > 0:
		p("Verdict: transcripts are reachable without anti-bot tokens for %s of the videos checked: keep transcripts (go).", pctStr(share(anyText, len(recs))))
	case withTracks+offered == 0:
		p("Verdict: inconclusive. No caption file or transcript could be tried (player unavailable or no captions on these videos); run the check again later.")
	default:
		p("Verdict: no transcript came back without an anti-bot token (%d caption files and %d transcript requests tried): drop transcripts (no-go), as plan E12 says.", withTracks, offered)
	}
	p("")
}

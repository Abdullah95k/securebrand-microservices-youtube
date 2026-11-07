package contract_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collect"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/comments"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/enrich"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/fakeyt"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/proxypool"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/worker"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
)

const chID = "UCfakeCONTRACT0000000001"

var (
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
	prod  = conformance.Options{Production: true}
)

func fakeChannel() fakeyt.FakeChannel {
	return fakeyt.FakeChannel{ID: chID, Handle: "@fakecontract", Title: "قناة تجريبية", Country: "Iraq", Subscribers: "25K subscribers",
		Videos: []fakeyt.FakeVideo{
			{ID: "fakeKV1", Title: "أخبار بغداد اليوم", Views: 1200, Likes: 40, Ago: "2 hours ago", LengthSec: 300, CommentsTxt: "3",
				PublishDate: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
				Comments: []fakeyt.FakeComment{
					{ID: "fakeKC1", Text: "first", Author: "UCfakeKAUTHOR00000000001", Ago: "1 hour ago", Likes: "1.2K", Pinned: true,
						Replies: []fakeyt.FakeComment{{ID: "fakeKC1.r1", Text: "a reply", Author: chID, Ago: "30 minutes ago", Likes: "2"}}},
					{ID: "fakeKC2", Text: "second", Author: "UCfakeKAUTHOR00000000002", Ago: "1 hour ago", Likes: "0"},
				}},
			{ID: "fakeKV2", Title: "recipe of the day", Views: 50, Likes: 2, Ago: "3 days ago", LengthSec: 120, CommentsTxt: "0"},
			{ID: "fakeKV3", Title: "short from Basra", Views: 999, Ago: "5 days ago", LengthSec: 30, Short: true, CommentsTxt: "0"},
		},
		Posts:     []fakeyt.FakePost{{ID: "UgkxFAKEKPOST1", Text: "منشور عن بغداد", Ago: "1 day ago", Likes: "1.2K", CommentsTxt: "0"}},
		Playlists: []fakeyt.FakePlaylist{{ID: "PLfakeKLIST01", Title: "تقارير بغداد", VideoIDs: []string{"fakeKV1", "fakeKV2"}}},
	}
}

type rig struct {
	pool *pgxpool.Pool
	live *kwstore.Live
}

// collectAll lists the fake channel and runs the collector, the comment
// engine and enrichment against the fake YouTube for a few seconds.
func collectAll(t *testing.T) *rig {
	t.Helper()
	yt := fakeyt.New()
	t.Cleanup(yt.Close)
	yt.ServeChannels([]fakeyt.FakeChannel{fakeChannel()}, 2, 2)
	px := fakeyt.NewForwardProxy("a")
	t.Cleanup(px.Close)
	ps, err := proxypool.ParseList([]string{px.ProxyLine() + " country=IQ"})
	if err != nil {
		t.Fatal(err)
	}
	pp := proxypool.New(ps, proxypool.Config{MinInterval: time.Millisecond, Jitter: 1e-12, DisableBrake: true,
		FailoverDelayMin: time.Millisecond, FailoverDelayMax: 2 * time.Millisecond, TransientBackoff: time.Millisecond}, 1)
	debug, err := phase0.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := innertube.New(innertube.Options{Pool: pp, BaseURL: yt.URL, CDNBase: yt.URL})
	fetch := &phase0.Collector{Client: client, Store: debug, Now: time.Now, Log: quiet}
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := kwstore.ImportSeed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	live := &kwstore.Live{}
	if _, err := live.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	svc := &collect.Service{Pool: pool, Fetch: fetch, Sup: sup, Log: quiet, WithPlayer: true,
		Write: &writer.Writer{Pool: pool, Sup: sup, KW: live}}
	h := svc.Handlers()
	for kind, f := range (&comments.Engine{Pool: pool, Client: client, Sup: sup, Log: quiet}).Handlers() {
		h[kind] = f
	}
	for kind, f := range (&enrich.Enricher{Pool: pool, Sup: sup, Log: quiet, Fetch: fetch}).Handlers() {
		h[kind] = f
	}
	if err := registry.AddInput(ctx, pool, registry.Input{Input: "@fakecontract", Tier: policy.A}); err != nil {
		t.Fatal(err)
	}
	w := &worker.Worker{Pool: pool, Owner: "test", Handlers: h, Concurrency: 4, Idle: 10 * time.Millisecond, Lease: time.Minute, Log: quiet}
	wctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	_ = w.Run(wctx)
	return &rig{pool: pool, live: live}
}

// Everything the collector wrote maps to messages the contract accepts in
// production, keyed as the contract says.
func TestEveryEventConformsEndToEnd(t *testing.T) {
	r := collectAll(t)
	rows, err := outbox.Unsent(context.Background(), r.pool, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	m := &contract.Mapper{Options: prod, Keywords: func() contract.KeywordIDs { return r.live.Matcher() }}
	kinds := map[string]int{}
	topics := map[string]int{}
	byID := map[string]contract.Message{}
	for _, row := range rows {
		kinds[row.Kind]++
		msgs, err := m.Map(row)
		if err != nil {
			t.Errorf("event %d (%s %s): %v", row.ID, row.Kind, row.EntityID, err)
			continue
		}
		for _, msg := range msgs {
			topics[msg.Topic]++
			env := envelopeOf(t, msg)
			byID[msg.Topic+" "+env.GetId()] = msg
			if env.GetVersion() != row.ObservedAt.UnixMicro() || env.GetProvenance().GetCollector() != "yts" ||
				env.GetProvenance().GetExitCountry() != "IQ" || len(env.GetProvenance().GetJobIds()) != 1 {
				t.Errorf("%s %s: envelope %v", msg.Topic, env.GetId(), env)
			}
			// Comments and counts are keyed by their post.
			if (msg.Topic == contract.TopicComment || msg.Topic == contract.TopicMetrics) && !strings.HasPrefix(msg.Key, "yt:post:") {
				t.Errorf("%s keyed %q, want the post", msg.Topic, msg.Key)
			}
		}
	}
	for _, k := range []string{"channel", "channel_snapshot", "video", "video_metrics", "post", "post_metrics", "comment",
		"comment_metrics", "playlist", "author"} {
		if kinds[k] == 0 {
			t.Errorf("no %s events (events by kind: %v)", k, kinds)
		}
	}
	for _, tp := range []string{contract.TopicAccount, contract.TopicAccountSnapshot, contract.TopicPost, contract.TopicComment,
		contract.TopicMetrics, contract.TopicPlaylist} {
		if topics[tp] == 0 {
			t.Errorf("no %s messages (by topic: %v)", tp, topics)
		}
	}

	// The Baghdad video: relevant, with the contract's keyword ID, exact
	// time, and its account.
	if p, ok := byID[contract.TopicPost+" yt:post:fakeKV1"].Msg.(*contentv1.Post); !ok {
		t.Error("no post for fakeKV1")
	} else {
		if got := p.GetRelevance().GetMatchedKeywordIds(); !contains(got, "baghdad:ar:بغداد") || !p.GetRelevance().GetRelevant() {
			t.Errorf("relevance %v", p.GetRelevance())
		}
		if p.GetPublishedAt().GetPrecision() != contentv1.TimePrecision_TIME_PRECISION_EXACT || p.GetAccountId() != "yt:account:"+chID ||
			p.GetType() != contentv1.PostType_POST_TYPE_VIDEO || p.GetVideo().GetDurationMs() != 300000 {
			t.Errorf("post %v", p)
		}
	}
	// The reply: depth 1 under its thread, by the channel itself.
	if c, ok := byID[contract.TopicComment+" yt:comment:fakeKC1.r1"].Msg.(*contentv1.Comment); !ok {
		t.Error("no comment for the reply")
	} else if c.GetDepth() != 1 || c.GetRootCommentId() != "yt:comment:fakeKC1" || !c.GetFlags().GetByPostOwner() ||
		c.GetPostId() != "yt:post:fakeKV1" || !strings.Contains(c.GetUrl(), "lc=fakeKC1.r1") {
		t.Errorf("reply %v", c)
	}
	// "1.2K" likes are rounded; the estimate sits at the start of its hour.
	if c, ok := byID[contract.TopicComment+" yt:comment:fakeKC1"].Msg.(*contentv1.Comment); ok {
		if l := c.GetCounts().GetLikes(); l.GetExactness() != contentv1.CountExactness_COUNT_EXACTNESS_ROUNDED || l.GetValue() != 1200 {
			t.Errorf("likes %v", l)
		}
		if pa := c.GetPublishedAt(); pa.GetPrecision() != contentv1.TimePrecision_TIME_PRECISION_HOUR || pa.GetAt().AsTime().Minute() != 0 {
			t.Errorf("published %v", pa)
		}
	}
	// The channel: a full account with its scope; commenters not monitored.
	if a, ok := byID[contract.TopicAccount+" yt:account:"+chID].Msg.(*contentv1.Account); !ok {
		t.Error("no account for the channel")
	} else if a.GetScope() != contentv1.CollectionScope_COLLECTION_SCOPE_FULL || a.GetHandle() != "@fakecontract" ||
		a.GetCounts().GetFollowers().GetExactness() != contentv1.CountExactness_COUNT_EXACTNESS_ROUNDED {
		t.Errorf("channel account %v", a)
	}
	if a, ok := byID[contract.TopicAccount+" yt:account:UCfakeKAUTHOR00000000001"].Msg.(*contentv1.Account); !ok {
		t.Error("no account for the commenter")
	} else if a.GetScope() != contentv1.CollectionScope_COLLECTION_SCOPE_NOT_MONITORED {
		t.Errorf("commenter account %v", a)
	}
	// The playlist and its two videos, from position 0.
	if p, ok := byID[contract.TopicPlaylist+" yt:playlist:PLfakeKLIST01"].Msg.(*contentv1.Playlist); !ok {
		t.Error("no playlist")
	} else if len(p.GetItems()) != 2 || p.GetItems()[0].GetPosition() != 0 {
		t.Errorf("playlist items %v", p.GetItems())
	}
}

func envelopeOf(t *testing.T, msg contract.Message) *contentv1.Envelope {
	t.Helper()
	switch v := msg.Msg.(type) {
	case *contentv1.Account:
		return v.GetEnvelope()
	case *contentv1.AccountSnapshot:
		return v.GetEnvelope()
	case *contentv1.Post:
		return v.GetEnvelope()
	case *contentv1.Comment:
		return v.GetEnvelope()
	case *contentv1.Metrics:
		return v.GetEnvelope()
	case *contentv1.Playlist:
		return v.GetEnvelope()
	}
	t.Fatalf("unexpected %s", msg.Type())
	return nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func row(kind, id, key, payload string) outbox.Row {
	return outbox.Row{ID: 1, Kind: kind, EntityID: id, PartitionKey: key, ObservedAt: time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC),
		Payload: []byte(payload), Provenance: []byte(`{"collector":"yts","collector_version":"dev","parser_version":"1","job_id":"video/x/1","job_kind":"video","operations":["next"]}`)}
}

// A removal and a deletion are partial events: keys and status, no
// content.
func TestRemovalsArePartial(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	msgs, err := m.Map(row("video", "fakeKV9", "fakeKV9", `{"id":"fakeKV9","channel_id":"`+chID+`","kind":"video","title":"kept",
		"published_at":"2026-11-01T10:00:00+00:00","published_precision_s":0,"removed_at":"2026-11-07T08:00:00+00:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	p := msgs[0].Msg.(*contentv1.Post)
	if !p.GetEnvelope().GetPartial() || p.GetTitle() != nil || p.GetStatus().GetAvailability() != contentv1.Availability_AVAILABILITY_UNAVAILABLE {
		t.Errorf("removed video %v", p)
	}
	msgs, err = m.Map(row("comment", "fakeKC9", "fakeKV9", `{"id":"fakeKC9","parent_kind":"video","parent_id":"fakeKV9","channel_id":"`+chID+`",
		"thread_id":"fakeKC8","text":"kept","deleted_at":"2026-11-07T08:00:00+00:00"}`))
	if err != nil {
		t.Fatal(err)
	}
	c := msgs[0].Msg.(*contentv1.Comment)
	if !c.GetEnvelope().GetPartial() || c.GetText() != nil || c.GetStatus().GetAvailability() != contentv1.Availability_AVAILABILITY_DELETED ||
		c.GetDepth() != 1 || msgs[0].Key != "yt:post:fakeKV9" {
		t.Errorf("deleted comment %v", c)
	}
}

// Estimates sit at the start of their unit (D-013): days at 00:00 Iraq
// time, 21:00 UTC the day before.
func TestDayEstimateAtIraqMidnight(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	msgs, err := m.Map(row("post", "UgkxFAKE9", "UgkxFAKE9", `{"id":"UgkxFAKE9","channel_id":"`+chID+`","text":"t",
		"published_at":"2026-11-04T09:30:00+00:00","published_precision_s":86400,"published_text":"3 days ago"}`))
	if err != nil {
		t.Fatal(err)
	}
	pa := msgs[0].Msg.(*contentv1.Post).GetPublishedAt()
	want := time.Date(2026, 11, 3, 21, 0, 0, 0, time.UTC)
	if !pa.GetAt().AsTime().Equal(want) || pa.GetPrecision() != contentv1.TimePrecision_TIME_PRECISION_DAY || pa.GetDisplayed() != "3 days ago" {
		t.Errorf("published %v, want %v at day precision", pa, want)
	}
}

// A listed channel's commenter events add no account: the channel's own
// events describe it.
func TestListedCommenterNotDuplicated(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	msgs, err := m.Map(row("author", chID, chID, `{"id":"`+chID+`","handle":"@fakecontract","monitored":true}`))
	if err != nil || len(msgs) != 0 {
		t.Errorf("monitored commenter: %d messages, err %v", len(msgs), err)
	}
}

// What the contract refuses is not published: Map says why.
func TestRejectedByTheContract(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	_, err := m.Map(row("channel", chID, chID, `{"id":"`+chID+`","kind":"core","status":"listed"}`))
	var rej *contract.Rejected
	if !errors.As(err, &rej) || rej.Topic != contract.TopicAccount || !strings.Contains(err.Error(), "neither display_name nor handle") {
		t.Errorf("a channel with no name: %v", err)
	}
	if _, err := m.Map(row("nonsense", "x", "x", `{}`)); !errors.Is(err, contract.ErrUnknownKind) {
		t.Errorf("unknown kind: %v", err)
	}
}

// The same row always gives the same event ID; a different read time, a
// different one.
func TestEventIDsAreStable(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	r := row("video_metrics", "fakeKV1", "fakeKV1", `{"counts":{"views":10},"channel_id":"`+chID+`"}`)
	a, _ := m.Map(r)
	b, _ := m.Map(r)
	r.ObservedAt = r.ObservedAt.Add(time.Second)
	c, _ := m.Map(r)
	ida, idb, idc := envelopeOf(t, a[0]).GetEventId(), envelopeOf(t, b[0]).GetEventId(), envelopeOf(t, c[0]).GetEventId()
	if ida == "" || ida != idb || ida == idc {
		t.Errorf("event IDs %q %q %q", ida, idb, idc)
	}
}

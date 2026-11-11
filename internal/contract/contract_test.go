package contract_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	coveragev1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/coverage/v1"
	discoveryv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/discovery/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collecttest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/contract"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

const chID = collecttest.ChannelID

var prod = conformance.Options{Production: true}

// Everything the collector wrote maps to messages the contract accepts in
// production, keyed as the contract says.
func TestEveryEventConformsEndToEnd(t *testing.T) {
	r := collecttest.Run(t)
	rows, err := outbox.Unsent(context.Background(), r.Pool, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	m := &contract.Mapper{Options: prod, Keywords: func() contract.KeywordIDs { return r.Live.Matcher() }}
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
		"comment_metrics", "playlist", "author", "media"} {
		if kinds[k] == 0 {
			t.Errorf("no %s events (events by kind: %v)", k, kinds)
		}
	}
	for _, tp := range []string{contract.TopicAccount, contract.TopicAccountSnapshot, contract.TopicPost, contract.TopicComment,
		contract.TopicMetrics, contract.TopicPlaylist, contract.TopicMedia} {
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
	case *contentv1.Media:
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

// A channel whose page gave no name is a partial account: what is known,
// and nothing blanked (11 November: the contract refused 3 such events).
func TestNamelessChannelIsPartial(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	msgs, err := m.Map(row("channel", chID, chID, `{"id":"`+chID+`","kind":"core","status":"listed","tier":"A",
		"description":"kept","subscribers_min":1200,"subscribers_text":"1.2K subscribers"}`))
	if err != nil {
		t.Fatal(err)
	}
	a := msgs[0].Msg.(*contentv1.Account)
	if !a.GetEnvelope().GetPartial() || a.GetDisplayName() != "" || a.GetHandle() != "" || a.GetBio().GetText() != "kept" ||
		a.GetCounts().GetFollowers() == nil || a.GetScope() != contentv1.CollectionScope_COLLECTION_SCOPE_FULL {
		t.Errorf("nameless channel %v", a)
	}
	msgs, err = m.Map(row("channel", chID, chID, `{"id":"`+chID+`","kind":"core","status":"listed","title":"Fake channel"}`))
	if err != nil {
		t.Fatal(err)
	}
	if a := msgs[0].Msg.(*contentv1.Account); a.GetEnvelope().GetPartial() || a.GetDisplayName() != "Fake channel" {
		t.Errorf("a named channel is a full account: %v", a)
	}
}

// What the contract refuses is not published: Map says why.
func TestRejectedByTheContract(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	_, err := m.Map(row("video_metrics", "fakeKV7", "fakeKV7", `{"counts":{"views":-10},"channel_id":"`+chID+`"}`))
	var rej *contract.Rejected
	if !errors.As(err, &rej) || rej.Topic != contract.TopicMetrics || !strings.Contains(err.Error(), "counts.views is negative") {
		t.Errorf("a negative count: %v", err)
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

// A candidate: its source, its evidence (an official-API hit only by its
// keyword), the outcome, and the API's 30-day terms when only the API
// found it. The contract's Candidate check passes.
func TestCandidates(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	for _, tc := range []struct {
		name, payload string
		outcome       discoveryv1.Outcome
		source        discoveryv1.Source
		apiOnly       bool
	}{
		{"API only, pending", `{"channel_id":"UCfakeCAND00000000000001","first_found":"2026-11-01T09:00:00+00:00","sources":["api_search"],
			"status":"new","evidence":[{"engine":"api","keyword":"baghdad","found_at":"2026-11-01T09:00:00+00:00"}]}`,
			discoveryv1.Outcome_OUTCOME_PENDING, discoveryv1.Source_SOURCE_OFFICIAL_API_SEARCH, true},
		{"website, added as mention", `{"channel_id":"UCfakeCAND00000000000001","first_found":"2026-11-01T09:00:00+00:00",
			"sources":["web_search","api_search"],"status":"listed","checked_at":"2026-11-02T09:00:00+00:00","decision":"mention: Iraq",
			"channel":{"kind":"mention","status":"listed"},
			"evidence":[{"engine":"web","keyword":"baghdad","video_id":"fakeCANDv1","published_at":"2026-10-31T12:00:00+00:00"}]}`,
			discoveryv1.Outcome_OUTCOME_ADDED_RELEVANT_ONLY, discoveryv1.Source_SOURCE_KEYWORD_SEARCH, false},
		{"commenter, gone", `{"channel_id":"UCfakeCAND00000000000001","first_found":"2026-11-01T09:00:00+00:00","sources":["commenter"],
			"status":"no_match","checked_at":"2026-11-02T09:00:00+00:00","decision":"channel not found","evidence":[]}`,
			discoveryv1.Outcome_OUTCOME_UNAVAILABLE, discoveryv1.Source_SOURCE_COMMENTER, false},
	} {
		msgs, err := m.Map(row("candidate", "UCfakeCAND00000000000001", "UCfakeCAND00000000000001", tc.payload))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		c := msgs[0].Msg.(*discoveryv1.Candidate)
		if msgs[0].Topic != contract.TopicCandidate || msgs[0].Key != "yt:account:UCfakeCAND00000000000001" ||
			c.GetOutcome() != tc.outcome || c.GetSource() != tc.source || c.GetTargetPlatform() != "yt" {
			t.Errorf("%s: %s %s %v", tc.name, msgs[0].Topic, msgs[0].Key, c)
		}
		if got := c.GetProvenance().GetTermsExpireAt() != nil; got != tc.apiOnly {
			t.Errorf("%s: terms_expire_at set %v, want %v", tc.name, got, tc.apiOnly)
		}
		for _, e := range c.GetEvidence() {
			if e.GetVia() == "api_search" && (e.GetContentId() != "" || e.GetUrl() != "") {
				t.Errorf("%s: an API hit carries a video: %v", tc.name, e)
			}
			if e.GetVia() == "web_search" && (e.GetContentId() != "yt:post:fakeCANDv1" || e.GetContentTime().GetPrecision() != contentv1.TimePrecision_TIME_PRECISION_DAY) {
				t.Errorf("%s: website evidence %v", tc.name, e)
			}
		}
	}
}

// An hour's roll-up: its counters, its status and why, keyed by its job.
func TestCoverage(t *testing.T) {
	m := &contract.Mapper{Options: prod}
	msgs, err := m.Map(row("coverage", "comments@2026-11-07T08:00Z", "comments@2026-11-07T08:00Z",
		`{"hour":"2026-11-07T08:00:00+00:00","kind":"comments","jobs":40,"failures":3,"requests":120,"bytes":500000,"blocks":2,"run_ms":90000}`))
	if err != nil {
		t.Fatal(err)
	}
	c := msgs[0].Msg.(*coveragev1.JobCoverage)
	if msgs[0].Topic != contract.TopicCoverage || msgs[0].Key != "yts:comments@2026-11-07T08:00Z" || c.GetJobCount() != 40 ||
		c.GetStatus() != coveragev1.JobStatus_JOB_STATUS_PARTIAL || c.GetCounters()["requests"] != 120 || c.GetCounters()["blocked"] != 2 ||
		len(c.GetReasons()) != 2 || c.GetWindowTo().AsTime().Sub(c.GetWindowFrom().AsTime()) != time.Hour {
		t.Errorf("coverage %s %v", msgs[0].Key, c)
	}
}

// A stored image: its record (content.media, kept forever), its
// announcement to analysis with its object, deadline and consumers, and
// its deletion. Every one passes the contract's check.
func TestMedia(t *testing.T) {
	m := &contract.Mapper{Options: prod, MediaBucket: "sb-media", MediaPrefix: "yts/"}
	const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	base := `"kind":"thumb","entity_id":"fakeMEDv1","channel_id":"` + chID + `","sha256":"` + sha + `",
		"object_key":"thumb/fakeMEDv1/` + sha + `.jpg","source_url":"https://i.ytimg.com/vi/fakeMEDv1/hqdefault.jpg",
		"content_type":"image/jpeg","bytes":20000,"first_seen":"2026-11-07T08:00:00+00:00"`
	const id = "yt:media:thumb:fakeMEDv1:0123456789abcdef"

	msgs, err := m.Map(row("media", "thumb:fakeMEDv1:0123456789abcdef", "", `{`+base+`,"attempt":0}`))
	if err != nil {
		t.Fatal(err)
	}
	rec := msgs[0].Msg.(*contentv1.Media)
	if msgs[0].Topic != contract.TopicMedia || msgs[0].Key != id || rec.GetEnvelope().GetId() != id ||
		rec.GetOwnerId() != "yt:post:fakeMEDv1" || rec.GetOwnerKind() != "post" || rec.GetRole() != contentv1.MediaRole_MEDIA_ROLE_THUMBNAIL ||
		rec.GetType() != contentv1.MediaType_MEDIA_TYPE_IMAGE || rec.GetSizeBytes() != 20000 || rec.GetEnvelope().GetContentHash() == "" {
		t.Errorf("media %s %v", msgs[0].Key, rec)
	}

	msgs, err = m.Map(row("media_ready", "thumb:fakeMEDv1:0123456789abcdef", "",
		`{`+base+`,"announced_at":"2026-11-07T09:00:00+00:00","delete_after":"2026-11-10T09:00:00+00:00","attempt":1}`))
	if err != nil {
		t.Fatal(err)
	}
	ready := msgs[0].Msg.(*mediav1.MediaReady)
	if msgs[0].Topic != contract.TopicMediaReady || msgs[0].Key != id || ready.GetAccountId() != "yt:account:"+chID ||
		ready.GetObject().GetBucket() != "sb-media" || ready.GetObject().GetKey() != "yts/thumb/fakeMEDv1/"+sha+".jpg" ||
		ready.GetObject().GetSha256() != sha || ready.GetAttempt() != 1 || !contains(ready.GetRequiredConsumers(), "sb-analysis") ||
		ready.GetDeleteAfter().AsTime().Sub(ready.GetStoredAt().AsTime()) != 73*time.Hour {
		t.Errorf("media_ready %s %v", msgs[0].Key, ready)
	}
	// Without a bucket nothing can be announced.
	if _, err := (&contract.Mapper{Options: prod}).Map(row("media_ready", "x", "", `{`+base+`,"attempt":1}`)); err == nil {
		t.Error("an announcement without a bucket must fail")
	}
	// A deadline under 24 hours after the file was stored is refused.
	var rej *contract.Rejected
	if _, err := m.Map(row("media_ready", "x", "", `{`+base+`,"delete_after":"2026-11-07T09:00:00+00:00","attempt":1}`)); !errors.As(err, &rej) {
		t.Errorf("a deadline an hour after storing: %v", err)
	}

	for reason, want := range map[string]mediav1.DeleteReason{"processed": mediav1.DeleteReason_DELETE_REASON_PROCESSED,
		"expired": mediav1.DeleteReason_DELETE_REASON_EXPIRED} {
		msgs, err = m.Map(row("media_deleted", "thumb:fakeMEDv1:0123456789abcdef", "",
			`{`+base+`,"attempt":1,"deleted_at":"2026-11-08T09:00:00+00:00","delete_reason":"`+reason+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		del := msgs[0].Msg.(*mediav1.MediaDeleted)
		if msgs[0].Topic != contract.TopicMediaDeleted || msgs[0].Key != id || del.GetReason() != want || del.GetDeletedAt() == nil {
			t.Errorf("media_deleted %s %v", reason, del)
		}
	}

	// Avatars and banners belong to their channel; a commenter's avatar to
	// the commenter.
	for kind, role := range map[string]contentv1.MediaRole{"avatar": contentv1.MediaRole_MEDIA_ROLE_PROFILE_PICTURE,
		"banner": contentv1.MediaRole_MEDIA_ROLE_BANNER, "author_avatar": contentv1.MediaRole_MEDIA_ROLE_PROFILE_PICTURE} {
		msgs, err := m.Map(row("media", kind, "", `{"kind":"`+kind+`","entity_id":"UCfakeMEDAUTHOR000000001","sha256":"`+sha+`",
			"object_key":"k","source_url":"https://yt3.ggpht.com/fake","content_type":"image/jpeg","bytes":1,"first_seen":"2026-11-07T08:00:00+00:00"}`))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		rec := msgs[0].Msg.(*contentv1.Media)
		if rec.GetOwnerId() != "yt:account:UCfakeMEDAUTHOR000000001" || rec.GetOwnerKind() != "account" || rec.GetRole() != role {
			t.Errorf("%s: %v", kind, rec)
		}
		// Handed off too (the owner, 11 November): announced to analysis
		// as the account's picture, in production mode.
		msgs, err = m.Map(row("media_ready", kind, "", `{"kind":"`+kind+`","entity_id":"UCfakeMEDAUTHOR000000001","sha256":"`+sha+`",
			"object_key":"`+kind+`/k.jpg","source_url":"https://yt3.ggpht.com/fake","content_type":"image/jpeg","bytes":1,
			"first_seen":"2026-11-07T08:00:00+00:00","announced_at":"2026-11-07T09:00:00+00:00","delete_after":"2026-11-10T09:00:00+00:00","attempt":1}`))
		if err != nil {
			t.Fatalf("%s ready: %v", kind, err)
		}
		ready := msgs[0].Msg.(*mediav1.MediaReady)
		if ready.GetOwnerId() != "yt:account:UCfakeMEDAUTHOR000000001" || ready.GetAccountId() != "yt:account:UCfakeMEDAUTHOR000000001" ||
			ready.GetRole() != role || ready.GetObject().GetKey() != "yts/"+kind+"/k.jpg" {
			t.Errorf("%s ready: %v", kind, ready)
		}
	}
}

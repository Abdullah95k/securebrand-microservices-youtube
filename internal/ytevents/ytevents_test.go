package ytevents_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	ytv1 "github.com/Abdullah95k/securebrand-microservices-youtube/gen/go/securebrand/yt/v1"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/collecttest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytevents"
)

// Every outbox event of a real run maps to its yt.* message with nothing
// left over: strict mode fails on a column the message lacks.
func TestEveryEventMapsStrictly(t *testing.T) {
	r := collecttest.Run(t)
	rows, err := outbox.Unsent(context.Background(), r.Pool, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	m := ytevents.Mapper{Strict: true}
	topics := map[string]int{}
	byKey := map[string]*ytevents.Message{}
	for _, row := range rows {
		msg, err := m.Map(row)
		if err != nil {
			t.Errorf("event %d (%s %s): %v", row.ID, row.Kind, row.EntityID, err)
			continue
		}
		if msg == nil {
			if row.Kind != outbox.KindCommentMetrics && row.Kind != outbox.KindCandidate && row.Kind != outbox.KindCoverage {
				t.Errorf("%s event %d mapped to nothing", row.Kind, row.ID)
			}
			continue
		}
		topics[msg.Topic]++
		byKey[msg.Topic+" "+row.EntityID] = msg
		ev := msg.Msg.ProtoReflect().Get(msg.Msg.ProtoReflect().Descriptor().Fields().ByName("event")).Message().Interface().(*ytv1.Event)
		if ev.GetEventId() == "" || ev.GetVersion() != row.ObservedAt.UnixMicro() || ev.GetOutboxId() != row.ID ||
			ev.GetProvenance().GetCollector() != "yts" || len(ev.GetProvenance().GetExitCountries()) == 0 {
			t.Errorf("%s event %d: header %v", row.Kind, row.ID, ev)
		}
		// Round trip: the message survives the wire.
		raw, err := proto.Marshal(msg.Msg)
		if err != nil {
			t.Fatal(err)
		}
		back := msg.Msg.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(raw, back); err != nil || !proto.Equal(back, msg.Msg) {
			t.Errorf("%s event %d does not survive the wire: %v", row.Kind, row.ID, err)
		}
	}
	for _, tp := range ytevents.Topics() {
		if topics[tp] == 0 {
			t.Errorf("no %s messages (by topic: %v)", tp, topics)
		}
	}

	v, ok := byKey["yt.video.v1 fakeKV1"].Msg.(*ytv1.Video)
	if !ok {
		t.Fatal("no yt.video for fakeKV1")
	}
	if v.GetTitle() != "أخبار بغداد اليوم" || v.GetChannelId() != collecttest.ChannelID || v.GetPublishedPrecisionS() != 0 ||
		v.GetDurationS() != 300 || !v.GetRelevant() || len(v.GetMatchedKeywords()) == 0 || v.GetPublishedAt() == nil {
		t.Errorf("video %v", v)
	}
	if k := byKey["yt.video.v1 fakeKV1"].Key; k != "yt:post:fakeKV1" {
		t.Errorf("video key %q", k)
	}
	c, ok := byKey["yt.comment.v1 fakeKC1.r1"].Msg.(*ytv1.Comment)
	if !ok || c.GetThreadId() != "fakeKC1" || !c.GetAuthorIsCreator() || byKey["yt.comment.v1 fakeKC1.r1"].Key != "yt:post:fakeKV1" {
		t.Errorf("reply %v", c)
	}
	ch, ok := byKey["yt.channel.v1 "+collecttest.ChannelID].Msg.(*ytv1.Channel)
	if !ok || ch.GetHandle() != "@fakecontract" || ch.GetSubscribersMin() != 25000 || ch.GetTier() != "A" || ch.GetCountry() != "Iraq" {
		t.Errorf("channel %v", ch)
	}
	pl, ok := byKey["yt.playlist.v1 PLfakeKLIST01"].Msg.(*ytv1.Playlist)
	if !ok || len(pl.GetItems()) != 2 || pl.GetItems()[0].GetPosition() != 1 {
		t.Errorf("playlist %v", pl)
	}
}

// A column the message doesn't have yet: left out unless strict.
func TestUnknownColumns(t *testing.T) {
	row := outbox.Row{ID: 7, Kind: outbox.KindVideo, EntityID: "fakeX", PartitionKey: "fakeX", ObservedAt: time.Now(),
		Payload: []byte(`{"id":"fakeX","channel_id":"UCfakeX","kind":"video","a_new_column":1}`)}
	if _, err := (ytevents.Mapper{Strict: true}).Map(row); err == nil {
		t.Error("strict mode must refuse an unknown column")
	}
	msg, err := ytevents.Mapper{}.Map(row)
	if err != nil || msg.Msg.(*ytv1.Video).GetId() != "fakeX" {
		t.Errorf("lenient mode: %v, %v", msg, err)
	}
	for _, k := range []string{outbox.KindCommentMetrics, outbox.KindCandidate, outbox.KindCoverage} {
		if msg, err := (ytevents.Mapper{}).Map(outbox.Row{Kind: k}); msg != nil || err != nil {
			t.Errorf("%s has no yt.* topic: %v, %v", k, msg, err)
		}
	}
	// First seen through the official API, never read on the website: the
	// API's title must not go out.
	api := row
	api.Payload = []byte(`{"id":"fakeX","channel_id":"UCfakeX","kind":"video","title":"an API title","source":"api_search"}`)
	if msg, err := (ytevents.Mapper{}).Map(api); msg != nil || err != nil {
		t.Errorf("an API-sourced video not yet read: %v, %v", msg, err)
	}
}

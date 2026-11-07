// Package ytevents maps yts's outbox events to the yt.* platform events
// (REQ-010 fix 12, standard E2): every field yts holds, in the messages of
// proto/securebrand/yt/v1. Each message mirrors its outbox payload field
// for field, so a payload is read straight into its message; the relay
// publishes it next to the content.* message from the same row.
//
// A comment's counts alone have no yt.* topic: content.metrics.v1 carries
// them, and the next yt.comment.v1 message has them too. Discovery's
// candidates and the coverage roll-ups have none either: their contract
// topics (discovery.candidate.v1, collection.coverage.v1) carry everything.
//
// A video first stored from an official-API search hit and never read on
// the website (title and time still the API's) is not sent: its first
// website read is.
package ytevents

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	ytv1 "github.com/Abdullah95k/securebrand-microservices-youtube/gen/go/securebrand/yt/v1"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

// Message is one yt.* message, ready for Kafka.
type Message struct {
	Topic string
	Key   string // the same contract ID as the content.* message's key
	Msg   proto.Message
}

// Type is the message's full name, for the sb-type header.
func (m Message) Type() string { return string(m.Msg.ProtoReflect().Descriptor().FullName()) }

type kind struct {
	topic string
	msg   func() proto.Message
	key   func(r outbox.Row) string
}

func account(r outbox.Row) string  { return "yt:account:" + r.EntityID }
func post(r outbox.Row) string     { return "yt:post:" + r.EntityID }
func postKey(r outbox.Row) string  { return "yt:post:" + r.PartitionKey }
func playlist(r outbox.Row) string { return "yt:playlist:" + r.EntityID }

// kinds maps the outbox kinds to their topics. comment_metrics has none.
var kinds = map[string]kind{
	outbox.KindChannel:         {"yt.channel.v1", func() proto.Message { return &ytv1.Channel{} }, account},
	outbox.KindChannelSnapshot: {"yt.channel_snapshot.v1", func() proto.Message { return &ytv1.ChannelSnapshot{} }, account},
	outbox.KindVideo:           {"yt.video.v1", func() proto.Message { return &ytv1.Video{} }, post},
	outbox.KindVideoMetrics:    {"yt.video_metrics.v1", func() proto.Message { return &ytv1.VideoMetrics{} }, post},
	outbox.KindPost:            {"yt.post.v1", func() proto.Message { return &ytv1.Post{} }, post},
	outbox.KindPostMetrics:     {"yt.post_metrics.v1", func() proto.Message { return &ytv1.PostMetrics{} }, post},
	outbox.KindComment:         {"yt.comment.v1", func() proto.Message { return &ytv1.Comment{} }, postKey},
	outbox.KindPlaylist:        {"yt.playlist.v1", func() proto.Message { return &ytv1.Playlist{} }, playlist},
	outbox.KindAuthor:          {"yt.author_profile.v1", func() proto.Message { return &ytv1.AuthorProfile{} }, account},
}

// Topics lists the yt.* topics yts publishes.
func Topics() []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, k.topic)
	}
	return out
}

// Mapper maps outbox rows.
type Mapper struct {
	// Strict refuses a payload with a field the message lacks (tests: a
	// column added to a table must be added to its message). Off, such a
	// field is left out until the message has it.
	Strict bool
}

// Map returns the yt.* message for one outbox row; nil for a kind with no
// yt.* topic (a comment's counts).
func (m Mapper) Map(r outbox.Row) (*Message, error) {
	switch r.Kind {
	case outbox.KindCommentMetrics, outbox.KindCandidate, outbox.KindCoverage:
		return nil, nil // no yt.* topic: the contract's message is the only one
	}
	k, ok := kinds[r.Kind]
	if !ok {
		return nil, fmt.Errorf("ytevents: unknown outbox kind %q (event %d)", r.Kind, r.ID)
	}
	opt := protojson.UnmarshalOptions{DiscardUnknown: !m.Strict}
	msg := k.msg()
	if err := opt.Unmarshal(r.Payload, msg); err != nil {
		return nil, fmt.Errorf("ytevents: %s event %d: %w", r.Kind, r.ID, err)
	}
	if v, ok := msg.(*ytv1.Video); ok && v.GetSource() == "api_search" && v.LastChecked == nil && v.RemovedAt == nil {
		return nil, nil
	}
	prov := &ytv1.Provenance{}
	if len(r.Provenance) > 0 {
		if err := opt.Unmarshal(r.Provenance, prov); err != nil {
			return nil, fmt.Errorf("ytevents: provenance of event %d: %w", r.ID, err)
		}
	}
	version := r.ObservedAt.UnixMicro()
	h := sha256.Sum256([]byte("yt|" + strconv.FormatInt(r.ID, 10) + "|" + r.Kind + "|" + r.EntityID + "|" + strconv.FormatInt(version, 10)))
	ev := &ytv1.Event{EventId: hex.EncodeToString(h[:]), Kind: r.Kind, ObservedAt: timestamppb.New(r.ObservedAt),
		Version: version, Provenance: prov, OutboxId: r.ID}
	pr := msg.ProtoReflect()
	fd := pr.Descriptor().Fields().ByName("event")
	if fd == nil {
		return nil, fmt.Errorf("ytevents: %s has no event field", pr.Descriptor().FullName())
	}
	pr.Set(fd, protoreflect.ValueOfMessage(ev.ProtoReflect()))
	return &Message{Topic: k.topic, Key: k.key(r), Msg: msg}, nil
}

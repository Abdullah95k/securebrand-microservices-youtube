// Package contract maps yts's outbox events to the platform's unified
// contract (REQ-021, contract v1.2.0, github.com/abdullah95k/securebrand-contracts):
// content.account, content.account_snapshot, content.post, content.comment,
// content.metrics, content.playlist and content.media messages, the media
// hand-off's content.media.ready and content.media.deleted,
// discovery.candidate and collection.coverage, each checked by the contract's conformance package
// (where it has a check) before it may be published.
//
// The rules it follows (the contract README, "The rules collectors
// follow"):
//   - IDs are "yt:<kind>:<YouTube ID>"; comments and counts are keyed by
//     their post, so a post's comments and counts stay in order.
//   - The version is the time of the read in microseconds; the event ID is
//     derived from the ID, that time, the message kind and the content hash,
//     so a message sent twice has the same event ID.
//   - Estimated times sit at the start of their unit (owner D-013), and a
//     count YouTube abbreviated is ROUNDED.
//   - A removal or deletion is a partial event: the keys and the status. An
//     item whose publish time isn't known yet is partial too.
//   - Fields YouTube doesn't show logged out stay unset.
//   - Commenters are NOT_MONITORED accounts, unless they are listed
//     channels, whose own events describe them.
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/abdullah95k/securebrand-contracts/conformance"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	coveragev1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/coverage/v1"
	discoveryv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/discovery/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/keywords"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

// The contract's topics.
const (
	TopicAccount         = "content.account.v1"
	TopicAccountSnapshot = "content.account_snapshot.v1"
	TopicPost            = "content.post.v1"
	TopicComment         = "content.comment.v1"
	TopicMetrics         = "content.metrics.v1"
	TopicPlaylist        = "content.playlist.v1"
)

// Message is one contract message, ready for Kafka.
type Message struct {
	Topic string
	Key   string // the contract ID the topic is keyed by
	Msg   proto.Message
}

// Type is the message's full name, for the sb-type header.
func (m Message) Type() string { return string(m.Msg.ProtoReflect().Descriptor().FullName()) }

// KeywordIDs gives the contract's keyword IDs for the keywords a text
// matched (kwstore.Matcher).
type KeywordIDs interface {
	ContractIDs(text string, matched []string) []string
}

// Mapper maps outbox rows.
type Mapper struct {
	// Keywords returns the current keyword matcher; nil: relevance carries
	// no keyword IDs.
	Keywords func() KeywordIDs
	// Options for the conformance checks: Production and Canary while the
	// canary runs (owner D-012), then Production alone.
	Options conformance.Options
	// MediaBucket and MediaPrefix: where the stored images are (the bucket,
	// and the prefix in front of every key; media.Location), for
	// content.media.ready. MediaConsumers must acknowledge each file
	// (DefaultConsumers when empty).
	MediaBucket, MediaPrefix string
	MediaConsumers           []string
}

// Rejected is a message the contract's checks refused. It is not
// published; the outbox keeps it with the reason.
type Rejected struct {
	Topic string
	Err   error
}

func (r *Rejected) Error() string { return fmt.Sprintf("%s rejected: %v", r.Topic, r.Err) }

// ErrUnknownKind: the outbox row's kind has no mapping.
var ErrUnknownKind = errors.New("contract: unknown outbox kind")

// Map returns the contract messages for one outbox row, every one checked.
// A row can map to none: a listed channel's commenter events, whose
// channel events describe the account.
func (m *Mapper) Map(r outbox.Row) ([]Message, error) {
	var src sourceTrail
	if len(r.Provenance) > 0 {
		if err := json.Unmarshal(r.Provenance, &src); err != nil {
			return nil, fmt.Errorf("contract: provenance of event %d: %w", r.ID, err)
		}
	}
	b := builder{m: m, row: r, src: src}
	var msgs []Message
	var err error
	switch r.Kind {
	case outbox.KindChannel:
		msgs, err = b.channel()
	case outbox.KindChannelSnapshot:
		msgs, err = b.channelSnapshot()
	case outbox.KindVideo:
		msgs, err = b.video()
	case outbox.KindVideoMetrics:
		msgs, err = b.videoMetrics()
	case outbox.KindPost:
		msgs, err = b.post()
	case outbox.KindPostMetrics:
		msgs, err = b.postMetrics()
	case outbox.KindComment:
		msgs, err = b.comment()
	case outbox.KindCommentMetrics:
		msgs, err = b.commentMetrics()
	case outbox.KindPlaylist:
		msgs, err = b.playlist()
	case outbox.KindAuthor:
		msgs, err = b.author()
	case outbox.KindCandidate:
		msgs, err = b.candidate()
	case outbox.KindCoverage:
		msgs, err = b.coverage()
	case outbox.KindMedia:
		msgs, err = b.media()
	case outbox.KindMediaReady:
		msgs, err = b.mediaReady()
	case outbox.KindMediaDeleted:
		msgs, err = b.mediaDeleted()
	default:
		return nil, fmt.Errorf("%w %q (event %d)", ErrUnknownKind, r.Kind, r.ID)
	}
	if err != nil {
		return nil, fmt.Errorf("contract: %s event %d: %w", r.Kind, r.ID, err)
	}
	for _, msg := range msgs {
		if err := m.check(msg); err != nil {
			return nil, &Rejected{Topic: msg.Topic, Err: err}
		}
	}
	return msgs, nil
}

func (m *Mapper) check(msg Message) error {
	switch v := msg.Msg.(type) {
	case *contentv1.Account:
		return conformance.Account(v, m.Options)
	case *contentv1.AccountSnapshot:
		return conformance.AccountSnapshot(v, m.Options)
	case *contentv1.Post:
		return conformance.Post(v, m.Options)
	case *contentv1.Comment:
		return conformance.Comment(v, m.Options)
	case *contentv1.Metrics:
		return conformance.Metrics(v, m.Options)
	case *contentv1.Playlist:
		return conformance.Playlist(v, m.Options)
	case *contentv1.Media:
		return conformance.Media(v, m.Options)
	case *mediav1.MediaReady:
		return conformance.MediaReady(v, m.Options)
	case *mediav1.MediaDeleted:
		return conformance.MediaDeleted(v, m.Options)
	case *discoveryv1.Candidate:
		return conformance.Candidate(v, m.Options)
	case *coveragev1.JobCoverage:
		return nil // the contract has no check for it
	}
	return fmt.Errorf("no check for %s", msg.Type())
}

// builder maps one row.
type builder struct {
	m   *Mapper
	row outbox.Row
	src sourceTrail
}

func (b *builder) decode(v any) error { return json.Unmarshal(b.row.Payload, v) }

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// envelope is the envelope of a message about id, without its event ID and
// content hash (finish sets them).
func (b *builder) envelope(kind, id, native string, partial bool) *contentv1.Envelope {
	at := b.row.ObservedAt
	return &contentv1.Envelope{
		Platform: Platform, Kind: kind, Id: id, NativeId: native,
		ObservedAt: timestamppb.New(at), Version: at.UnixMicro(), Partial: partial,
		Provenance: &contentv1.Provenance{
			Collector: b.src.Collector, CollectorVersion: b.src.CollectorVersion, ParserVersion: b.src.ParserVersion,
			SourceOperation: first(b.src.Operations), Acquisition: contentv1.Acquisition_ACQUISITION_SCRAPED_LOGGED_OUT,
			Mode: crawlMode(b.src.JobKind), RawRef: first(b.src.RawRefs), ExitCountry: first(b.src.ExitCountries),
			JobIds: nonEmpty(b.src.JobID),
		},
	}
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// finish sets the content hash (content messages: the message without its
// envelope, counts and coverage) and the event ID.
func finish(env *contentv1.Envelope, msg proto.Message, content bool) {
	if content {
		c := proto.Clone(msg)
		switch v := c.(type) {
		case *contentv1.Account:
			v.Envelope, v.Counts = nil, nil
		case *contentv1.Post:
			v.Envelope, v.Counts, v.Coverage = nil, nil, nil
		case *contentv1.Comment:
			v.Envelope, v.Counts = nil, nil
		case *contentv1.Playlist:
			v.Envelope, v.ItemCount = nil, nil
		}
		if raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(c); err == nil {
			h := sha256.Sum256(raw)
			env.ContentHash = hex.EncodeToString(h[:])
		}
	}
	h := sha256.Sum256([]byte(env.Id + "|" + strconv.FormatInt(env.Version, 10) + "|" + env.Kind + "|" + env.ContentHash))
	env.EventId = hex.EncodeToString(h[:])
}

func visible() *contentv1.Status {
	return &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_VISIBLE}
}

// gone is the status of something YouTube no longer shows: logged out,
// deleted, private and removed look alike (the contract's UNAVAILABLE).
func gone(since time.Time, reason string) *contentv1.Status {
	return &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_UNAVAILABLE, Reason: reason, Since: timestamppb.New(since)}
}

// relevance is why an item was collected: its keyword IDs (the stored
// spellings that matched, contract style) and the language rule.
func (b *builder) relevance(relevant *bool, matched []string, texts ...string) *contentv1.Relevance {
	if relevant == nil && len(matched) == 0 {
		return nil
	}
	r := &contentv1.Relevance{Relevant: relevant}
	if slices.Contains(matched, keywords.KurdishID) {
		r.MatchedRules = []string{keywords.KurdishID}
	}
	if b.m.Keywords != nil {
		if k := b.m.Keywords(); k != nil {
			r.MatchedKeywordIds = k.ContractIDs(strings.Join(texts, "\n"), matched)
		}
	}
	return r
}

// extras are the registered platform-only fields (registry/extras.yaml).
func extras(kv map[string]any) *structpb.Struct {
	clean := map[string]any{}
	for k, v := range kv {
		switch x := v.(type) {
		case nil:
		case *string:
			if x != nil && *x != "" {
				clean[k] = *x
			}
		case *bool:
			if x != nil {
				clean[k] = *x
			}
		default:
			clean[k] = v
		}
	}
	if len(clean) == 0 {
		return nil
	}
	s, err := structpb.NewStruct(clean)
	if err != nil {
		return nil
	}
	return s
}

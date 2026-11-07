package contract

import (
	"fmt"
	"time"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
)

// The contract's media topics (securebrand.media.v1; owner R5).
const (
	TopicMedia        = "content.media.v1"
	TopicMediaReady   = "content.media.ready.v1"
	TopicMediaDeleted = "content.media.deleted.v1"
)

// DefaultConsumers are the consumers that must acknowledge a file before
// it may be deleted (the registry's consumers).
var DefaultConsumers = []string{"sb-analysis"}

type mediaRow struct {
	Kind         string     `json:"kind"`
	EntityID     string     `json:"entity_id"`
	ChannelID    *string    `json:"channel_id"`
	SHA256       string     `json:"sha256"`
	ObjectKey    string     `json:"object_key"`
	SourceURL    string     `json:"source_url"`
	ContentType  string     `json:"content_type"`
	Bytes        int64      `json:"bytes"`
	FirstSeen    time.Time  `json:"first_seen"`
	DeleteAfter  *time.Time `json:"delete_after"`
	Attempt      int32      `json:"attempt"`
	DeletedAt    *time.Time `json:"deleted_at"`
	DeleteReason *string    `json:"delete_reason"`
}

func (m mediaRow) key() outbox.MediaKey {
	return outbox.MediaKey{Kind: m.Kind, EntityID: m.EntityID, SHA256: m.SHA256}
}

// mediaID is the contract ID of a stored image: "yt:media:" and its
// media key ("thumb:<video>:<first 16 hex of the SHA-256>").
func mediaID(k outbox.MediaKey) string { return Platform + ":media:" + k.ID() }

// owner is what an image belongs to, its kind, its account and its role:
// a thumbnail belongs to its video; an avatar or banner to its channel; a
// commenter's avatar to the commenter.
func (m mediaRow) owner() (id, kind, account string, role contentv1.MediaRole, err error) {
	switch m.Kind {
	case "thumb":
		if m.ChannelID != nil && *m.ChannelID != "" {
			account = accountID(*m.ChannelID)
		}
		return postID(m.EntityID), "post", account, contentv1.MediaRole_MEDIA_ROLE_THUMBNAIL, nil
	case "avatar", "author_avatar":
		return accountID(m.EntityID), "account", accountID(m.EntityID), contentv1.MediaRole_MEDIA_ROLE_PROFILE_PICTURE, nil
	case "banner":
		return accountID(m.EntityID), "account", accountID(m.EntityID), contentv1.MediaRole_MEDIA_ROLE_BANNER, nil
	}
	return "", "", "", 0, fmt.Errorf("unknown media kind %q", m.Kind)
}

// media: content.media, the record of a stored image (kept forever, after
// the file itself is gone).
func (b *builder) media() ([]Message, error) {
	var m mediaRow
	if err := b.decode(&m); err != nil {
		return nil, err
	}
	owner, kind, _, role, err := m.owner()
	if err != nil {
		return nil, err
	}
	k := m.key()
	id := mediaID(k)
	size := m.Bytes
	msg := &contentv1.Media{Envelope: b.envelope("media", id, k.ID(), false), OwnerId: owner, OwnerKind: kind,
		Type: contentv1.MediaType_MEDIA_TYPE_IMAGE, Role: role, PlatformKind: m.Kind, SourceUrl: m.SourceURL,
		Mime: m.ContentType, SizeBytes: &size}
	finish(msg.Envelope, msg, true)
	return []Message{{Topic: TopicMedia, Key: id, Msg: msg}}, nil
}

// mediaReady: content.media.ready, the announcement of a stored image to
// analysis, with its deadline and the consumers that must acknowledge it.
func (b *builder) mediaReady() ([]Message, error) {
	var m mediaRow
	if err := b.decode(&m); err != nil {
		return nil, err
	}
	owner, kind, account, role, err := m.owner()
	if err != nil {
		return nil, err
	}
	if b.m.MediaBucket == "" {
		return nil, fmt.Errorf("no media bucket configured")
	}
	consumers := b.m.MediaConsumers
	if len(consumers) == 0 {
		consumers = DefaultConsumers
	}
	k := m.key()
	id := mediaID(k)
	msg := &mediav1.MediaReady{
		EventId: eventID("media_ready", id, b.row.ObservedAt.UnixMicro()), Platform: Platform,
		MediaId: id, OwnerId: owner, OwnerKind: kind, AccountId: account,
		Type: contentv1.MediaType_MEDIA_TYPE_IMAGE, Role: role,
		Object: &mediav1.StorageObject{Bucket: b.m.MediaBucket, Key: b.m.MediaPrefix + m.ObjectKey, SizeBytes: m.Bytes,
			Sha256: m.SHA256, ContentType: m.ContentType},
		StoredAt: timestamppb.New(m.FirstSeen), RequiredConsumers: consumers,
		Provenance: b.provenance(contentv1.Acquisition_ACQUISITION_SCRAPED_LOGGED_OUT), Attempt: m.Attempt,
	}
	if m.DeleteAfter != nil {
		msg.DeleteAfter = timestamppb.New(*m.DeleteAfter)
	}
	return []Message{{Topic: TopicMediaReady, Key: id, Msg: msg}}, nil
}

// mediaDeleted: content.media.deleted, the file is gone from storage
// (every required consumer acknowledged it, or its deadline passed).
func (b *builder) mediaDeleted() ([]Message, error) {
	var m mediaRow
	if err := b.decode(&m); err != nil {
		return nil, err
	}
	k := m.key()
	id := mediaID(k)
	msg := &mediav1.MediaDeleted{EventId: eventID("media_deleted", id, b.row.ObservedAt.UnixMicro()), MediaId: id, Platform: Platform}
	switch deref(m.DeleteReason) {
	case "processed":
		msg.Reason = mediav1.DeleteReason_DELETE_REASON_PROCESSED
	case "expired":
		msg.Reason = mediav1.DeleteReason_DELETE_REASON_EXPIRED
	}
	if m.DeletedAt != nil {
		msg.DeletedAt = timestamppb.New(*m.DeletedAt)
	}
	return []Message{{Topic: TopicMediaDeleted, Key: id, Msg: msg}}, nil
}

// Package privacy holds the building blocks of plan §8.9:
//
//   - keyed hashes (HMAC-SHA256 with a server secret) of authors, channels
//     and items, used by the suppression list and by pseudonymized exports;
//   - the suppression list: erased authors, channels and items, checked
//     before anything is written so erased data never comes back;
//   - retention settings. Everything is kept forever (D18, D26); time limits,
//     redacting deleted comments and pseudonymized exports exist as
//     settings, all switched off.
//
// The secret is read from a file and never logged. Back it up: without it
// the suppression list cannot be checked.
package privacy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/secrets"
)

// Kinds of things that can be erased and suppressed.
const (
	Author  = "author"
	Channel = "channel"
	Video   = "video"
	Comment = "comment"
	Post    = "post"
)

// Kinds lists every kind.
var Kinds = []string{Author, Channel, Video, Comment, Post}

// ValidKind reports whether k is one of Kinds.
func ValidKind(k string) bool {
	for _, x := range Kinds {
		if k == x {
			return true
		}
	}
	return false
}

// MinSecretLen is the shortest accepted secret (32 bytes, e.g. the output
// of "openssl rand -hex 32" is 64 characters).
const MinSecretLen = 32

// Keyer computes keyed hashes.
type Keyer struct{ secret []byte }

// NewKeyer checks the secret's length.
func NewKeyer(secret []byte) (*Keyer, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("privacy: the secret must be at least %d bytes", MinSecretLen)
	}
	return &Keyer{secret: append([]byte(nil), secret...)}, nil
}

// LoadKeyer reads the secret from a file (owner-only permissions).
func LoadKeyer(path string) (*Keyer, error) {
	s, err := secrets.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return NewKeyer([]byte(s))
}

// Key is HMAC-SHA256(secret, kind 0x00 id).
func (k *Keyer) Key(kind, id string) []byte {
	m := hmac.New(sha256.New, k.secret)
	m.Write([]byte(kind))
	m.Write([]byte{0})
	m.Write([]byte(strings.TrimSpace(id)))
	return m.Sum(nil)
}

// AuthorKey is the pseudonym of an author in exports and webhook payloads:
// unique-author counts still work, names do not leave.
func (k *Keyer) AuthorKey(authorID string) string {
	return hex.EncodeToString(k.Key(Author, authorID))
}

// Suppressor answers "is this erased?" from an in-memory copy of the
// suppression list, refreshed from the database.
type Suppressor struct {
	k   *Keyer
	mu  sync.RWMutex
	set map[string]bool // kind + "\x00" + key
	at  time.Time
}

// NewSuppressor returns an empty suppressor; call Refresh before use.
func NewSuppressor(k *Keyer) *Suppressor {
	return &Suppressor{k: k, set: map[string]bool{}}
}

func setKey(kind string, key []byte) string { return kind + "\x00" + string(key) }

// Refresh reloads the active entries (those not lifted by an admin).
func (s *Suppressor) Refresh(ctx context.Context, q db.Querier) error {
	rows, err := q.Query(ctx, `SELECT kind, key FROM suppression_list WHERE override_at IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	set := map[string]bool{}
	for rows.Next() {
		var kind string
		var key []byte
		if err := rows.Scan(&kind, &key); err != nil {
			return err
		}
		set[setKey(kind, key)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.set, s.at = set, time.Now()
	s.mu.Unlock()
	return nil
}

// RefreshedAt is when the list was last loaded.
func (s *Suppressor) RefreshedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.at
}

// Suppressed reports whether id of kind is on the list. Empty IDs never are.
func (s *Suppressor) Suppressed(kind, id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	k := setKey(kind, s.k.Key(kind, id))
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.set[k]
}

// Add puts id on the list (again, if an admin had lifted it).
func (s *Suppressor) Add(ctx context.Context, q db.Querier, kind, id, reason string, requestID *int64) error {
	if !ValidKind(kind) {
		return fmt.Errorf("privacy: unknown kind %q", kind)
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(reason) == "" {
		return errors.New("privacy: suppression needs an ID and a reason")
	}
	key := s.k.Key(kind, id)
	_, err := q.Exec(ctx, `INSERT INTO suppression_list (kind, key, reason, request_id) VALUES ($1, $2, $3, $4)
		ON CONFLICT (kind, key) DO UPDATE SET reason = EXCLUDED.reason, request_id = EXCLUDED.request_id,
			created_at = now(), override_at = NULL, override_reason = NULL`, kind, key, reason, requestID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.set[setKey(kind, key)] = true
	s.mu.Unlock()
	return nil
}

// Override lifts a suppression; plan §8.9 requires a recorded reason.
func (s *Suppressor) Override(ctx context.Context, q db.Querier, kind, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("privacy: lifting a suppression needs a reason")
	}
	key := s.k.Key(kind, id)
	tag, err := q.Exec(ctx, `UPDATE suppression_list SET override_at = now(), override_reason = $3
		WHERE kind = $1 AND key = $2 AND override_at IS NULL`, kind, key, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("privacy: not on the suppression list")
	}
	s.mu.Lock()
	delete(s.set, setKey(kind, key))
	s.mu.Unlock()
	return nil
}

// Retention holds the retention settings. A zero value keeps everything
// forever, which is the default (D18, D26).
type Retention struct {
	// Days to keep each kind of data; 0 means forever.
	CommentsDays  int `json:"comments_days"`
	SnapshotsDays int `json:"snapshots_days"`
	ProfilesDays  int `json:"profiles_days"`
	// RedactDeletedComments blanks the text and author of comments their
	// authors deleted, instead of keeping them marked deleted.
	RedactDeletedComments bool `json:"redact_deleted_comments"`
	// PseudonymizeExports replaces author names with AuthorKey in exports
	// and webhook payloads.
	PseudonymizeExports bool `json:"pseudonymize_exports"`
}

// Forever reports whether no time limit is set.
func (r Retention) Forever() bool {
	return r.CommentsDays == 0 && r.SnapshotsDays == 0 && r.ProfilesDays == 0
}

// RetentionKey is the settings key.
const RetentionKey = "retention"

// LoadRetention reads the settings (the zero value when never set).
func LoadRetention(ctx context.Context, q db.Querier) (Retention, error) {
	var r Retention
	_, err := db.GetSetting(ctx, q, RetentionKey, &r)
	return r, err
}

// SaveRetention stores the settings.
func SaveRetention(ctx context.Context, q db.Querier, r Retention) error {
	if r.CommentsDays < 0 || r.SnapshotsDays < 0 || r.ProfilesDays < 0 {
		return errors.New("privacy: retention days cannot be negative")
	}
	return db.SetSetting(ctx, q, RetentionKey, r)
}

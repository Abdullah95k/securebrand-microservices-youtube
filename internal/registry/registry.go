// Package registry is the channel list (plan §8.2): which channels are
// collected, as core or mention channels, in which tier. Adding a channel
// schedules its jobs; unlisting removes them (its stored data is kept,
// D26). A channel on the suppression list (erased) cannot be added again
// unless an admin lifts the suppression with a reason.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
)

// ErrSuppressed: the channel was erased.
var ErrSuppressed = errors.New("registry: this channel was erased and is on the suppression list; " +
	"lift the suppression first (with a reason) if it really must come back")

// Kinds of listed channels.
const (
	Core    = "core"
	Mention = "mention"
)

// Input is a channel to add, as you would type it.
type Input struct {
	Input   string // @handle, channel URL or UC… ID
	Kind    string // Core or Mention
	Tier    policy.Tier
	AddedBy string // admin, seed, discovery
	Reason  string
}

func (in *Input) check() error {
	in.Input = strings.TrimSpace(in.Input)
	if in.Input == "" {
		return errors.New("registry: empty channel")
	}
	if in.Kind == "" {
		in.Kind = Core
	}
	if in.Kind != Core && in.Kind != Mention {
		return fmt.Errorf("registry: kind %q is not core or mention", in.Kind)
	}
	if in.Tier == "" {
		in.Tier = policy.C
		if in.Kind == Mention {
			in.Tier = policy.M
		}
	}
	if !in.Tier.Valid() {
		return fmt.Errorf("registry: tier %q", in.Tier)
	}
	if in.Kind == Mention && in.Tier != policy.M || in.Kind == Core && in.Tier == policy.M {
		return errors.New("registry: mention channels are tier M, and only they are")
	}
	if in.AddedBy == "" {
		in.AddedBy = "admin"
	}
	return nil
}

// AddInput records the input and queues a job that resolves it to a
// channel ID and lists the channel.
func AddInput(ctx context.Context, q db.Querier, in Input) error {
	if err := in.check(); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `INSERT INTO channel_inputs (input, kind, tier, added_by, reason) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (input) DO UPDATE SET kind = EXCLUDED.kind, tier = EXCLUDED.tier, reason = EXCLUDED.reason, error = NULL`,
		in.Input, in.Kind, string(in.Tier), in.AddedBy, in.Reason); err != nil {
		return err
	}
	return queue.Now(ctx, q, queue.KindResolve, in.Input, "", queue.P0)
}

// PendingInput is what the resolve job needs.
func PendingInput(ctx context.Context, q db.Querier, input string) (Input, error) {
	in := Input{Input: input}
	var tier string
	var reason *string
	err := q.QueryRow(ctx, `SELECT kind, tier, added_by, reason FROM channel_inputs WHERE input = $1`, input).
		Scan(&in.Kind, &tier, &in.AddedBy, &reason)
	if reason != nil {
		in.Reason = *reason
	}
	in.Tier = policy.Tier(tier)
	return in, err
}

// InputResolved records the outcome of a resolve job.
func InputResolved(ctx context.Context, q db.Querier, input, channelID string, cause error) error {
	if cause != nil {
		_, err := q.Exec(ctx, `UPDATE channel_inputs SET error = left($2, 300) WHERE input = $1`, input, cause.Error())
		return err
	}
	_, err := q.Exec(ctx, `UPDATE channel_inputs SET channel_id = $2, resolved_at = now(), error = NULL WHERE input = $1`, input, channelID)
	return err
}

// List puts a resolved channel on the list and schedules its jobs. It is
// also how discovery adds channels. A channel already listed gets the new
// kind and tier.
func List(ctx context.Context, q db.Querier, sup *privacy.Suppressor, channelID string, in Input) error {
	if err := in.check(); err != nil {
		return err
	}
	if !strings.HasPrefix(channelID, "UC") {
		return fmt.Errorf("registry: %q is not a channel ID", channelID)
	}
	if sup != nil && sup.Suppressed(privacy.Channel, channelID) {
		return ErrSuppressed
	}
	var handle *string
	if strings.HasPrefix(in.Input, "@") {
		handle = &in.Input
	}
	if _, err := q.Exec(ctx, `INSERT INTO channels (id, handle, kind, tier, status, added_by, added_reason)
		VALUES ($1, $2, $3, $4, 'listed', $5, $6)
		ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, tier = EXCLUDED.tier, status = 'listed',
			handle = COALESCE(channels.handle, EXCLUDED.handle),
			added_reason = COALESCE(EXCLUDED.added_reason, channels.added_reason)`,
		channelID, handle, in.Kind, string(in.Tier), in.AddedBy, nullIfEmpty(in.Reason)); err != nil {
		return err
	}
	return schedule(ctx, q, channelID, in.Kind, in.Tier)
}

func nullIfEmpty(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// schedule makes sure a listed channel has its recurring jobs. New rows
// start now (backfill before regular tab checks); existing rows keep their
// times.
func schedule(ctx context.Context, q db.Querier, channelID, kind string, tier policy.Tier) error {
	type job struct {
		kind string
		prio int
		due  time.Time
	}
	now := time.Now()
	jobs := []job{
		{queue.KindChannel, queue.P2, now},
		{queue.KindRSS, queue.P2, now},
		{queue.KindPosts, queue.P2, now},
		{queue.KindPlaylists, queue.P3, now},
	}
	if kind == Core {
		var done *time.Time
		if err := q.QueryRow(ctx, `SELECT backfill_done_at FROM channels WHERE id = $1`, channelID).Scan(&done); err != nil {
			return err
		}
		if done == nil {
			jobs = append(jobs, job{queue.KindBackfill, queue.P3, now})
		}
		jobs = append(jobs, job{queue.KindTabs, queue.P2, now.Add(policy.TabCheck(tier))})
	} else {
		// Mention channels have no tab checks or backfill.
		for _, k := range []string{queue.KindTabs, queue.KindBackfill} {
			if err := queue.Remove(ctx, q, k, channelID); err != nil {
				return err
			}
		}
	}
	for _, j := range jobs {
		if _, err := queue.Ensure(ctx, q, j.kind, channelID, channelID, j.prio, j.due); err != nil {
			return err
		}
	}
	return nil
}

// Channel is one row of the list.
type Channel struct {
	ID          string
	Handle      string
	Title       string
	Kind        string
	Tier        policy.Tier
	Status      string
	AddedBy     string
	FirstSeen   time.Time
	LastChecked *time.Time
}

// Channels returns the channels with the given status ("" = all), largest
// tiers first.
func Channels(ctx context.Context, q db.Querier, status string) ([]Channel, error) {
	rows, err := q.Query(ctx, `SELECT id, COALESCE(handle, ''), COALESCE(title, ''), kind, tier, status, added_by, first_seen, last_checked
		FROM channels WHERE ($1 = '' OR status = $1) ORDER BY tier, id`, status)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Channel, error) {
		var c Channel
		var tier string
		err := r.Scan(&c.ID, &c.Handle, &c.Title, &c.Kind, &tier, &c.Status, &c.AddedBy, &c.FirstSeen, &c.LastChecked)
		c.Tier = policy.Tier(tier)
		return c, err
	})
}

// Get returns one channel.
func Get(ctx context.Context, q db.Querier, id string) (Channel, error) {
	all, err := q.Query(ctx, `SELECT id, COALESCE(handle, ''), COALESCE(title, ''), kind, tier, status, added_by, first_seen, last_checked
		FROM channels WHERE id = $1`, id)
	if err != nil {
		return Channel{}, err
	}
	return pgx.CollectExactlyOneRow(all, func(r pgx.CollectableRow) (Channel, error) {
		var c Channel
		var tier string
		err := r.Scan(&c.ID, &c.Handle, &c.Title, &c.Kind, &tier, &c.Status, &c.AddedBy, &c.FirstSeen, &c.LastChecked)
		c.Tier = policy.Tier(tier)
		return c, err
	})
}

// Set changes a listed channel's kind and tier and adjusts its jobs.
func Set(ctx context.Context, q db.Querier, id, kind string, tier policy.Tier) error {
	in := Input{Input: id, Kind: kind, Tier: tier}
	if err := in.check(); err != nil {
		return err
	}
	tag, err := q.Exec(ctx, `UPDATE channels SET kind = $2, tier = $3, tier_manual = true WHERE id = $1 AND status = 'listed'`,
		id, in.Kind, string(in.Tier))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("registry: %s is not a listed channel", id)
	}
	return schedule(ctx, q, id, in.Kind, in.Tier)
}

// Unlist stops collecting a channel. Its data stays (D26); its jobs go.
func Unlist(ctx context.Context, q db.Querier, id, reason string) error {
	tag, err := q.Exec(ctx, `UPDATE channels SET status = 'dormant', added_reason = COALESCE(NULLIF($2, ''), added_reason)
		WHERE id = $1 AND status = 'listed'`, id, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("registry: %s is not a listed channel", id)
	}
	_, err = queue.RemoveChannel(ctx, q, id)
	return err
}

// Rearm gives every listed channel any recurring job it is missing (for
// example after a restore). It returns how many jobs it added.
func Rearm(ctx context.Context, q db.Querier) (int, error) {
	chans, err := Channels(ctx, q, "listed")
	if err != nil {
		return 0, err
	}
	var before, after int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM schedule`).Scan(&before); err != nil {
		return 0, err
	}
	for _, c := range chans {
		if err := schedule(ctx, q, c.ID, c.Kind, c.Tier); err != nil {
			return 0, err
		}
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM schedule`).Scan(&after); err != nil {
		return 0, err
	}
	return after - before, nil
}

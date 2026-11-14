package discovery

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/outbox"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// SourceSuggested is the candidates' source for channels found through
// suggested videos.
const SourceSuggested = "suggested"

// DefaultSuggestedDaily is the most new candidates a day (UTC) that
// suggested videos may add.
const DefaultSuggestedDaily = 500

// Suggested records the channels of a video's suggested videos as
// discovery candidates (the owner, 14 November: "for youtube, we need to add
// the suggested videos to the discovery as well, as it can lead to new
// channels that we didnt discover in other ways"). The suggestions come
// with the watch page yts already reads, so this makes no request.
//
// Suggestions drift away from Iraq quickly, so one counts only when its
// title matches the Iraq keywords (match: the writer's keyword matcher),
// and only when its channel is new to yts. A new channel becomes a
// candidate (source "suggested") and gets the usual candidate check, which
// lists it as Iraqi or mention, or not at all, by the same rules as search
// finds. At most daily new candidates per UTC day come this way; a hit on
// a candidate already waiting counts it again, as search hits do. It
// returns how many new candidates it added.
func Suggested(ctx context.Context, pool *pgxpool.Pool, match func(...string) (bool, []string),
	items []ytparse.VideoItem, daily int, now time.Time) (int, error) {
	if daily <= 0 || match == nil {
		return 0, nil
	}
	day := now.UTC().Truncate(24 * time.Hour)
	added := 0
	seen := map[string]bool{}
	for _, it := range items {
		if it.ChannelID == "" || seen[it.ChannelID] {
			continue
		}
		seen[it.ChannelID] = true
		if relevant, _ := match(it.Title); !relevant {
			continue
		}
		var known bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE id = $1)`, it.ChannelID).Scan(&known); err != nil {
			return added, err
		}
		if known {
			continue
		}
		ev := outbox.New(ctx, now)
		var isNew, full bool
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE candidates SET hits = hits + 1, last_found = $2,
					sources = CASE WHEN $3 = ANY(sources) THEN sources ELSE array_append(sources, $3::text) END
				WHERE channel_id = $1`, it.ChannelID, now, SourceSuggested)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				var today int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM candidates WHERE $1 = ANY(sources) AND first_found >= $2`,
					SourceSuggested, day).Scan(&today); err != nil {
					return err
				}
				if today >= daily {
					full = true
					return nil
				}
				if _, err := tx.Exec(ctx, `INSERT INTO candidates (channel_id, first_found, last_found, sources)
					VALUES ($1, $2, $2, ARRAY[$3::text]) ON CONFLICT (channel_id) DO NOTHING`, it.ChannelID, now, SourceSuggested); err != nil {
					return err
				}
				isNew = true
			}
			_, err = ev.Content(ctx, tx, outbox.Candidates, it.ChannelID)
			return err
		})
		if err != nil {
			return added, err
		}
		if full {
			return added, nil // the day's limit: the rest wait for tomorrow's readings
		}
		if isNew {
			added++
			if _, err := queue.Ensure(ctx, pool, queue.KindCandidate, it.ChannelID, it.ChannelID, queue.P3, now); err != nil {
				return added, err
			}
		}
	}
	return added, nil
}

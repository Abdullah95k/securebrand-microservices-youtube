package collect

import (
	"context"
	"errors"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// posts reads the channel's Posts tab, newest first, until a page brings
// nothing new (at most 5 pages; the first run reads up to 50). New posts of
// core channels, and relevant posts of mention channels, get their comments
// read.
func (s *Service) posts(ctx context.Context, j queue.Job) (time.Time, error) {
	ci, listed, err := s.channelInfo(ctx, j.EntityID)
	if err != nil || !listed {
		return time.Time{}, err
	}
	next := s.now().Add(s.stretch(policy.Posts(ci.tier), ci.tier))
	var cost phase0.Cost
	ch, err := s.Fetch.Channel(ctx, j.EntityID, &cost)
	if err != nil {
		return time.Time{}, err
	}
	tab, ok := ch.Channel.Tabs["posts"]
	if !ok {
		if tab, ok = ch.Channel.Tabs["community"]; !ok {
			return next, nil // no Posts tab
		}
	}
	var known int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM posts WHERE channel_id = $1`, j.EntityID).Scan(&known)
	maxPages := 5
	if known == 0 {
		maxPages = 50 // first run: the backlog
	}
	r, err := s.Fetch.Client.Call(ctx, innertube.Browse, map[string]any{"browseId": j.EntityID, "params": tab.Params})
	if err != nil {
		return time.Time{}, err
	}
	prefer := r.Proxy
	data := r.Data
	for page := 0; page < maxPages; page++ {
		ps, token := ytparse.ParsePosts(data, s.now())
		fresh := 0
		for _, p := range ps {
			out, err := s.Write.Post(ctx, j.EntityID, p)
			if errors.Is(err, writer.ErrSuppressed) {
				continue
			}
			if err != nil {
				return time.Time{}, err
			}
			if out.New {
				fresh++
			}
			if ci.kind == registry.Mention && !out.Relevant {
				continue
			}
			prio := queue.P3
			if out.New && known > 0 {
				prio = queue.P1
			}
			if _, err := queue.Ensure(ctx, s.Pool, queue.KindPostComments, p.ID, j.EntityID, prio, s.now()); err != nil {
				return time.Time{}, err
			}
		}
		if token == "" || fresh == 0 && known > 0 {
			break
		}
		r, err := s.Fetch.Client.CallPrefer(ctx, innertube.Browse, map[string]any{"continuation": token}, prefer)
		if err != nil {
			return time.Time{}, err
		}
		prefer, data = r.Proxy, r.Data
	}
	return next, nil
}

// playlists reads the Playlists tab and, for core channels (or relevant
// playlists of mention channels), the videos in each playlist.
func (s *Service) playlists(ctx context.Context, j queue.Job) (time.Time, error) {
	ci, listed, err := s.channelInfo(ctx, j.EntityID)
	if err != nil || !listed {
		return time.Time{}, err
	}
	next := s.now().Add(s.stretch(policy.Playlists(ci.tier), ci.tier))
	var cost phase0.Cost
	ch, err := s.Fetch.Channel(ctx, j.EntityID, &cost)
	if err != nil {
		return time.Time{}, err
	}
	tab, ok := ch.Channel.Tabs["playlists"]
	if !ok {
		return next, nil
	}
	r, err := s.Fetch.Client.Call(ctx, innertube.Browse, map[string]any{"browseId": j.EntityID, "params": tab.Params})
	if err != nil {
		return time.Time{}, err
	}
	var all []ytparse.Playlist
	data, prefer := r.Data, r.Proxy
	for page := 0; page < 20; page++ {
		pls, token := ytparse.ParsePlaylists(data)
		all = append(all, pls...)
		if token == "" || len(pls) == 0 {
			break
		}
		r, err := s.Fetch.Client.CallPrefer(ctx, innertube.Browse, map[string]any{"continuation": token}, prefer)
		if err != nil {
			return time.Time{}, err
		}
		data, prefer = r.Data, r.Proxy
	}
	for _, pl := range all {
		relevant, err := s.Write.Playlist(ctx, j.EntityID, pl)
		if errors.Is(err, writer.ErrSuppressed) {
			return time.Time{}, nil
		}
		if err != nil {
			return time.Time{}, err
		}
		if ci.kind == registry.Mention && !relevant {
			continue
		}
		if err := s.playlistItems(ctx, pl.ID, prefer); err != nil {
			return time.Time{}, err
		}
	}
	return next, nil
}

func (s *Service) playlistItems(ctx context.Context, playlistID, prefer string) error {
	r, err := s.Fetch.Client.CallPrefer(ctx, innertube.Browse, map[string]any{"browseId": "VL" + playlistID}, prefer)
	if err != nil {
		return err
	}
	var items []ytparse.PlaylistEntry
	complete := false
	data := r.Data
	prefer = r.Proxy
	for page := 0; page < 50; page++ {
		got, token := ytparse.ParsePlaylistItems(data)
		items = append(items, got...)
		if token == "" {
			complete = true
			break
		}
		r, err := s.Fetch.Client.CallPrefer(ctx, innertube.Browse, map[string]any{"continuation": token}, prefer)
		if err != nil {
			return err
		}
		data, prefer = r.Data, r.Proxy
	}
	return s.Write.PlaylistItems(ctx, playlistID, items, complete)
}

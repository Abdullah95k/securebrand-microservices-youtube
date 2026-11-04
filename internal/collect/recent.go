package collect

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/phase0"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

// tabParamsTTL: a channel's tab parameters (read from its channel page) are
// reused this long by RecentUploads.
const tabParamsTTL = 24 * time.Hour

// recentWindow: RecentUploads looks at uploads this recent.
const recentWindow = 48 * time.Hour

type cachedTabs struct {
	tabs map[string]ytparse.Tab
	at   time.Time
}

// tabCache holds channels' tab parameters (the zero value is ready to use).
type tabCache struct {
	mu sync.Mutex
	m  map[string]cachedTabs
}

func (s *Service) tabParams(ctx context.Context, channelID string, cost *phase0.Cost) (map[string]ytparse.Tab, error) {
	s.recentTabs.mu.Lock()
	c, ok := s.recentTabs.m[channelID]
	s.recentTabs.mu.Unlock()
	if ok && s.now().Sub(c.at) < tabParamsTTL {
		return c.tabs, nil
	}
	ch, err := s.Fetch.Channel(ctx, channelID, cost)
	if err != nil {
		return nil, err
	}
	s.recentTabs.mu.Lock()
	if s.recentTabs.m == nil {
		s.recentTabs.m = map[string]cachedTabs{}
	}
	s.recentTabs.m[channelID] = cachedTabs{tabs: ch.Channel.Tabs, at: s.now()}
	s.recentTabs.mu.Unlock()
	return ch.Channel.Tabs, nil
}

func (s *Service) forgetTabs(channelID string) {
	s.recentTabs.mu.Lock()
	delete(s.recentTabs.m, channelID)
	s.recentTabs.mu.Unlock()
}

// RecentUploads reads the first page of a channel's Videos, Shorts and Live
// tabs and stores the uploads it has not seen, queued the way a feed entry
// would be. It stands in for the channel's feed while YouTube's feed service
// answers "not found": in Phase 0 that happened for every channel at once,
// for about 9 hours at a time and roughly 40% of the time. It costs one
// request per tab (plus the channel page once a day) and returns the number
// of new uploads.
func (s *Service) RecentUploads(ctx context.Context, channelID string) (int, error) {
	ci, listed, err := s.channelInfo(ctx, channelID)
	if err != nil || !listed {
		return 0, err
	}
	var cost phase0.Cost
	tabs, err := s.tabParams(ctx, channelID, &cost)
	if err != nil {
		return 0, err
	}
	since := s.now().Add(-recentWindow)
	added := 0
	for _, key := range tabKeys {
		tab, ok := tabs[key]
		if !ok {
			continue
		}
		tr, err := s.Fetch.Tab(ctx, channelID, tab.Params, since, 1, &cost)
		if err != nil {
			s.forgetTabs(channelID) // the parameters may have changed: read the channel page next time
			return added, fmt.Errorf("%s tab: %w", key, err)
		}
		for _, it := range tr.Items {
			it.Kind = listingKind(key, it)
			isNew, err := s.Write.ListedVia(ctx, channelID, it, "tab")
			if errors.Is(err, writer.ErrSuppressed) {
				continue
			}
			if err != nil {
				return added, err
			}
			if !isNew {
				continue
			}
			added++
			if ci.kind == registry.Mention {
				if ok, _ := s.Write.Match(it.Title); !ok {
					continue // noted, not collected (as for feed entries)
				}
			}
			if err := queue.Now(ctx, s.Pool, queue.KindVideo, it.VideoID, channelID, queue.P0); err != nil {
				return added, err
			}
		}
	}
	return added, nil
}

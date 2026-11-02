package fakeyt

import (
	"fmt"
	"strings"
	"time"
)

// FakeSearchHit is one video a fake search returns.
type FakeSearchHit struct {
	VideoID   string
	ChannelID string // must start with UCfake
	Title     string
	Ago       string    // website: "3 days ago"
	Published time.Time // official API
}

// ServeSearch answers both the website's search (youtubei/v1/search, with
// continuation pages, tokens "SEARCHPAGE_<term number>_<page>") and the
// official API's search.list (pageToken "APIPAGE_<page>"). results maps a
// search term to its hits; pageSize hits are returned per page. Website
// pages also list each hit's channel once as a channelRenderer.
func (s *Server) ServeSearch(results map[string][]FakeSearchHit, pageSize int) {
	var terms []string
	for t := range results {
		terms = append(terms, t)
	}
	termNo := func(t string) int {
		for i, x := range terms {
			if x == t {
				return i
			}
		}
		return -1
	}
	page := func(hits []FakeSearchHit, n int) ([]FakeSearchHit, bool) {
		start, end := n*pageSize, (n+1)*pageSize
		if start >= len(hits) {
			return nil, false
		}
		if end > len(hits) {
			end = len(hits)
		}
		return hits[start:end], end < len(hits)
	}
	webItems := func(hits []FakeSearchHit) A {
		var items A
		seen := map[string]bool{}
		for _, h := range hits {
			items = append(items, M{"videoRenderer": M{
				"videoId": h.VideoID, "title": M{"runs": A{M{"text": h.Title}}},
				"ownerText":         M{"runs": A{M{"text": "Channel " + h.ChannelID[len(h.ChannelID)-4:], "navigationEndpoint": M{"browseEndpoint": M{"browseId": h.ChannelID}}}}},
				"publishedTimeText": M{"simpleText": h.Ago}, "viewCountText": M{"simpleText": "1,000 views"},
			}})
			if !seen[h.ChannelID] {
				seen[h.ChannelID] = true
				items = append(items, M{"channelRenderer": M{"channelId": h.ChannelID, "title": M{"simpleText": "Channel"}, "subscriberCountText": M{"simpleText": "1K subscribers"}}})
			}
		}
		return items
	}
	s.Handle("search", func(body map[string]any) (int, any) {
		n, term := 0, ""
		if tok, _ := body["continuation"].(string); tok != "" {
			var ti int
			if _, err := fmt.Sscanf(strings.TrimPrefix(tok, "SEARCHPAGE_"), "%d_%d", &ti, &n); err != nil || ti < 0 || ti >= len(terms) {
				return 400, M{"error": M{"code": 400}}
			}
			term = terms[ti]
		} else {
			term, _ = body["query"].(string)
		}
		hits, more := page(results[term], n)
		contents := A{M{"itemSectionRenderer": M{"contents": webItems(hits)}}}
		if more {
			contents = append(contents, Token(fmt.Sprintf("SEARCHPAGE_%d_%d", termNo(term), n+1)))
		}
		if n == 0 {
			return 200, M{"contents": M{"twoColumnSearchResultsRenderer": M{"primaryContents": M{"sectionListRenderer": M{"contents": contents}}}}}
		}
		return 200, M{"onResponseReceivedCommands": A{M{"appendContinuationItemsAction": M{"continuationItems": contents}}}}
	})
	s.Handle("api:search", func(q map[string]any) (int, any) {
		term, _ := q["q"].(string)
		n := 0
		if tok, _ := q["pageToken"].(string); tok != "" {
			fmt.Sscanf(strings.TrimPrefix(tok, "APIPAGE_"), "%d", &n)
		}
		hits, more := page(results[term], n)
		var items A
		for _, h := range hits {
			items = append(items, M{"id": M{"videoId": h.VideoID}, "snippet": M{
				"channelId": h.ChannelID, "channelTitle": "Channel", "title": h.Title, "publishedAt": h.Published.UTC().Format(time.RFC3339)}})
		}
		out := M{"items": items, "pageInfo": M{"totalResults": len(results[term])}}
		if more {
			out["nextPageToken"] = fmt.Sprintf("APIPAGE_%d", n+1)
		}
		return 200, out
	})
}

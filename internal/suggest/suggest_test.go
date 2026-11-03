package suggest_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/suggest"
)

const (
	core    = "UCfakeSUGGESTcore0000001"
	mention = "UCfakeSUGGESTmention0001"
)

func TestSuggestionsAndPrecision(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	_, _ = kwstore.ImportSeed(ctx, pool)
	_ = registry.List(ctx, pool, nil, core, registry.Input{Input: core, Tier: policy.B})
	_ = registry.List(ctx, pool, nil, mention, registry.Input{Input: mention, Kind: registry.Mention})
	n := 0
	add := func(channel, title string, relevant bool, age time.Duration, tags []string, kws []string) {
		n++
		if tags == nil {
			tags = []string{}
		}
		if kws == nil {
			kws = []string{}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO videos (id, channel_id, kind, title, tags, relevant, matched_keywords, first_seen)
			VALUES ($1, $2, 'video', $3, $4, $5, $6, now() - make_interval(secs => $7))`,
			fmt.Sprintf("fakeSG%04d", n), channel, title, tags, relevant, kws, age.Seconds()); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	for i := 0; i < 40; i++ { // a strong co-occurring hashtag
		add(core, "تغطية خاصة #الانتخابات", true, 3*day, []string{"مفوضية"}, []string{"iraq"})
	}
	add(core, "#الانتخابات elsewhere", false, 3*day, nil, nil)
	add(core, "#الانتخابات elsewhere", false, 3*day, nil, nil)
	for i := 0; i < 40; i++ { // generic words that also title unrelated videos
		add(core, "تغطية خاصة للمباراة", false, 3*day, nil, nil)
	}
	for i := 0; i < 12; i++ { // common everywhere: low precision
		add(core, "football highlights", true, 3*day, nil, nil)
	}
	for i := 0; i < 20; i++ {
		add(core, "football highlights", false, 3*day, nil, nil)
	}
	for i := 0; i < 6; i++ { // a spike today
		add(core, "فيضانات دهوك", true, 2*time.Hour, nil, nil)
	}
	now := time.Now()
	terms, err := suggest.Mine(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]suggest.Term{}
	for _, tm := range terms {
		by[tm.Normalized] = tm
	}
	if _, ok := by["football"]; ok {
		t.Error("a term common outside relevant items must not be suggested")
	}
	var hashtag, spike suggest.Term
	for k, v := range by {
		if v.Relevant == 40 && len(k) > 0 && k[0] == '#' {
			hashtag = v
		}
		if v.Trending {
			spike = v
		}
	}
	if hashtag.Term == "" || hashtag.Precision < 0.9 {
		t.Fatalf("the hashtag should be a strong suggestion: %+v (all: %+v)", hashtag, terms)
	}
	if spike.Term == "" || spike.Last24 != 6 {
		t.Errorf("a term spiking today should be trending: %+v", spike)
	}
	suggested, activated, err := suggest.Run(ctx, pool, now)
	if err != nil || suggested == 0 || activated != 2 {
		t.Fatalf("run: %d suggested, %d activated, err %v; the hashtag and the tag clear the threshold", suggested, activated, err)
	}
	var kinds string
	if err := pool.QueryRow(ctx, `SELECT string_agg(kind, ',' ORDER BY kind) FROM keywords WHERE source = 'suggestion'`).Scan(&kinds); err != nil || kinds != "hashtag,term" {
		t.Errorf("activated keyword kinds %q (err %v)", kinds, err)
	}
	// A rejected suggestion switches its keyword off and never comes back.
	var id int64
	_ = pool.QueryRow(ctx, `SELECT id FROM keyword_suggestions WHERE status = 'accepted' AND normalized LIKE '#%'`).Scan(&id)
	if err := suggest.Reject(ctx, pool, id); err != nil {
		t.Fatal(err)
	}
	var active bool
	_ = pool.QueryRow(ctx, `SELECT active FROM keywords WHERE source = 'suggestion' AND kind = 'hashtag'`).Scan(&active)
	if active {
		t.Error("rejecting must switch the keyword off")
	}
	if _, activated, _ := suggest.Run(ctx, pool, now); activated != 0 {
		t.Error("a rejected term must not be activated again")
	}
	// Switched off: nothing is activated automatically.
	_ = db.SetSetting(ctx, pool, suggest.AutoKey, false)

	// Precision: 25 mention-channel matches of "baghdad" alone (unconfirmed), 5 confirmed.
	for i := 0; i < 25; i++ {
		add(mention, "x", true, day, nil, []string{"baghdad"})
	}
	for i := 0; i < 5; i++ {
		add(mention, "x", true, day, nil, []string{"baghdad", "iraq"})
	}
	ps, err := suggest.MeasurePrecision(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		if p.Label == "baghdad" {
			found = true
			if p.Matches != 30 || p.Confirmed != 5 || !p.Low {
				t.Errorf("baghdad precision %+v; want 5 of 30 confirmed, flagged", p)
			}
		}
		if p.Label == "iraq" && p.Low {
			t.Errorf("iraq is confirmed by core channels: %+v", p)
		}
	}
	if !found {
		t.Error("baghdad precision missing")
	}
}

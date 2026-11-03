package kwstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/kwstore"
)

func TestSeedImportYourKeywordsAndLiveMatcher(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	n, err := kwstore.ImportSeed(ctx, pool)
	if err != nil || n < 40 {
		t.Fatalf("seed import: %d keywords, err %v", n, err)
	}
	if again, err := kwstore.ImportSeed(ctx, pool); err != nil || again != n {
		t.Fatalf("re-import: %d, err %v", again, err)
	}
	all, _ := kwstore.List(ctx, pool)
	if len(all) != n {
		t.Fatalf("a re-import must not duplicate: %d rows", len(all))
	}
	var live kwstore.Live
	if rebuilt, err := live.Refresh(ctx, pool); err != nil || !rebuilt {
		t.Fatalf("first refresh: %v %v", rebuilt, err)
	}
	if ok, ids := live.Matcher().Match("أخبار بغداد اليوم"); !ok || len(ids) == 0 {
		t.Errorf("Baghdad should match: %v %v", ok, ids)
	}
	if rebuilt, _ := live.Refresh(ctx, pool); rebuilt {
		t.Error("nothing changed: no rebuild")
	}

	// Your own brand keyword, added live.
	id, err := kwstore.Add(ctx, pool, kwstore.New{Label: "Zqbrand", Kind: "brand", Variants: []string{"Zqbrand", "زيكيوبراند"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := live.Matcher().Match("zqbrand review"); ok {
		t.Error("not refreshed yet")
	}
	if rebuilt, _ := live.Refresh(ctx, pool); !rebuilt {
		t.Error("a new keyword must rebuild the matcher")
	}
	ok, ids := live.Matcher().Match("ZQBRAND review")
	if !ok || len(ids) != 1 || !strings.HasPrefix(ids[0], "kw-") {
		t.Errorf("brand match %v %v", ok, ids)
	}
	// Ambiguous keywords need a second keyword.
	amb := true
	if err := kwstore.Update(ctx, pool, id, kwstore.Change{Ambiguous: &amb}); err != nil {
		t.Fatal(err)
	}
	live.Refresh(ctx, pool)
	if ok, _ := live.Matcher().Match("zqbrand review"); ok {
		t.Error("an ambiguous keyword alone must not count")
	}
	if ok, _ := live.Matcher().Match("zqbrand في بغداد"); !ok {
		t.Error("an ambiguous keyword with a second one counts")
	}
	// Switched off: no match; removed: gone.
	off := false
	_ = kwstore.Update(ctx, pool, id, kwstore.Change{Active: &off})
	live.Refresh(ctx, pool)
	if _, ids := live.Matcher().Match("zqbrand"); len(ids) != 0 {
		t.Errorf("a switched-off keyword matched: %v", ids)
	}
	if err := kwstore.Remove(ctx, pool, id); err != nil {
		t.Fatal(err)
	}
	if err := kwstore.Remove(ctx, pool, all[0].ID); err == nil {
		t.Error("seed keywords are switched off, not removed")
	}
	if _, err := kwstore.Add(ctx, pool, kwstore.New{Label: "x", Kind: "planet"}); err == nil {
		t.Error("unknown kinds must be refused")
	}
	if _, err := kwstore.Add(ctx, pool, kwstore.New{Label: "!!!", Kind: "topic"}); err == nil {
		t.Error("a keyword without letters must be refused")
	}
	terms, err := kwstore.SearchTerms(ctx, pool)
	if err != nil || len(terms) < n {
		t.Errorf("search terms: %d, err %v", len(terms), err)
	}
}

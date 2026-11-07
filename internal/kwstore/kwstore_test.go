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

// D28 (5 November 2026): Kurdish-language text is relevant on its own;
// "السليمانية" alone still needs a second Iraq keyword.
func TestKurdishAndSulaymaniyah(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := kwstore.ImportSeed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var live kwstore.Live
	if _, err := live.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	m := live.Matcher()
	ok, ids := m.Match("ڕۆژی 3 هێلکە بخۆی چی ڕودەدات ..؟؟ زۆر گرنگە بیزانی ..!!")
	if !ok || len(ids) != 1 || ids[0] != "lang-ckb" {
		t.Errorf("a Kurdish title with no keyword: %v %v; want relevant, matched by language", ok, ids)
	}
	if ok, _ := m.Match("مسجد التكية السليمانية في دمشق يعود لفتح أبوابه"); ok {
		t.Error("the Süleymaniye mosque in Damascus must not count")
	}
	if ok, _ := m.Match("محافظه سليمانيه"); !ok {
		t.Error("the governorate written without the article must count")
	}
	if ok, _ := m.Match("كانون الثاني 2022 محافظة السليمانية عراق"); !ok {
		t.Error("السليمانية with a second Iraq keyword counts")
	}
	if ok, _ := m.Match("وصفة الكبسة السعودية"); ok {
		t.Error("an unrelated Arabic title must not count")
	}
}

// The contract's keyword IDs (REQ-021): group, the matched spelling's
// language, and that spelling as stored, folded.
func TestContractIDs(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	if _, err := kwstore.ImportSeed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := kwstore.Add(ctx, pool, kwstore.New{Label: "Fake Brand", Kind: "brand", Variants: []string{"fakebrand"}, Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	var live kwstore.Live
	if _, err := live.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	m := live.Matcher()
	for _, tc := range []struct {
		text string
		want string
	}{
		{"أخبار بغداد اليوم", "baghdad:ar:بغداد"},
		{"news from Baghdad", "baghdad:en:baghdad"},
		{"هەولێر ئەمڕۆ", "erbil:ckb:ههولێر"},
		{"Hewlêr û Bexda", "baghdad:en:bexda erbil:kmr:hewler"},
		{"fakebrand launch in Erbil", "brand:en:fakebrand erbil:en:erbil"},
	} {
		_, matched := m.Match(tc.text)
		if got := strings.Join(m.ContractIDs(tc.text, matched), " "); got != tc.want {
			t.Errorf("%q: %q, want %q (matched %v)", tc.text, got, tc.want, matched)
		}
	}
	// Only the keywords the stored match names, and never a rule.
	if got := m.ContractIDs("بغداد", []string{"lang-ckb"}); got != nil {
		t.Errorf("a rule is not a keyword: %v", got)
	}
}

func TestVariantLanguage(t *testing.T) {
	for _, tc := range []struct{ variant, langs, want string }{
		{"بغداد", "ar,ckb,kmr,en", "ar"},
		{"بەغدا", "ar,ckb,kmr,en", "ckb"},
		{"Baghdad", "ar,ckb,kmr,en", "en"},
		{"Hewlêr", "ar,ckb,kmr,en", "kmr"},
		{"Irak", "tr", "tr"},
		{"بغداد", "", "ar"},
		{"123", "", "und"},
	} {
		if got := kwstore.VariantLanguage(tc.variant, tc.langs); got != tc.want {
			t.Errorf("VariantLanguage(%q, %q) = %q, want %q", tc.variant, tc.langs, got, tc.want)
		}
	}
}

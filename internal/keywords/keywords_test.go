package keywords

import (
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// Arabic letter variants.
		{"alef variants", "أحمد إبراهيم آمال ٱلعراق", "احمد ابراهيم امال العراق"},
		{"taa marbuta", "مدرسة", "مدرسه"},
		{"alef maqsura", "مستشفى", "مستشفي"},
		{"hamza seats", "مؤتمر رئيس", "موتمر رييس"},

		// Diacritics and tatweel.
		{"harakat", "العِرَاقُ", "العراق"},
		{"shadda and tanween", "مُحَمَّدٌ", "محمد"},
		{"superscript alef", "هٰذا", "هذا"},
		{"tatweel", "بـــغداد", "بغداد"},

		// Digits.
		{"eastern arabic digits", "عام ٢٠٢٤", "عام 2024"},
		{"persian digits", "سال ۱۴۰۳", "سال 1403"},

		// Kurdish (Sorani) letters.
		{"sorani e and ê", "هەولێر", "ههولير"},
		{"sorani keheh", "کەرکووک", "كهركووك"},
		{"sorani farsi yeh", "سلێمانی", "سليماني"},
		{"sorani o", "دهۆک", "دهوك"},
		{"sorani heavy l", "هەڵەبجە", "ههلهبجه"},
		{"sorani trilled r", "ڕانیە", "رانيه"},
		{"gaf stays", "گەرمیان", "گهرميان"},
		{"heh doachashmee", "ھەولێر", "ههولير"},

		// Latin folding.
		{"kurmanji", "Hewlêr û Duhok", "hewler u duhok"},
		{"kurmanji and turkish", "Kerkûk Kerkük KERKÜK", "kerkuk kerkuk kerkuk"},
		{"turkish dotless and dotted i", "Iraklı İstanbul ÇAĞ", "irakli istanbul cag"},
		{"turkish s cedilla", "Şengal Çiya", "sengal ciya"},
		{"french accents", "Café Élysée", "cafe elysee"},
		{"circumflex and umlaut", "Mâl Öz", "mal oz"},
		{"decomposed accent", "Café", "cafe"},
		{"transliteration marks", "Baʿqūbah", "baqubah"},

		// Hashtags and punctuation.
		{"arabic hashtag", "#العراق", "العراق"},
		{"latin hashtags", "#Iraq #Basra", "iraq basra"},
		{"hashtag with underscore", "#بغداد_اليوم", "بغداد اليوم"},
		{"double hash", "##iraq", "iraq"},
		{"arabic punctuation", "بغداد، البصرة؛ الموصل؟", "بغداد البصره الموصل"},
		{"english punctuation", "Iraq's capital: Baghdad!", "iraq s capital baghdad"},
		{"turkish apostrophe suffix", "Kerkük'te", "kerkuk te"},
		{"hyphen", "Al-Qadisiyyah", "al qadisiyyah"},
		{"whitespace", "  many   spaces\t\nhere  ", "many spaces here"},
		{"emoji and flags", "Iraq🇮🇶!!", "iraq"},

		// Invisible characters.
		{"zero-width non-joiner", "می‌خواهم", "ميخواهم"},
		{"direction mark", "‏بغداد", "بغداد"},
		{"zero-width space separates", "Iraq​Basra", "iraq basra"},

		// Nothing left.
		{"empty", "", ""},
		{"only punctuation", "!!! ... ؟", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Normalize(tc.in)
			if got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if again := Normalize(got); again != got {
				t.Errorf("Normalize is not idempotent: %q -> %q", got, again)
			}
		})
	}
}

// hitIDs returns the sorted, de-duplicated keyword IDs in hits.
func hitIDs(hits []Hit) []string {
	seen := map[string]bool{}
	ids := []string{}
	for _, h := range hits {
		if !seen[h.KeywordID] {
			seen[h.KeywordID] = true
			ids = append(ids, h.KeywordID)
		}
	}
	sort.Strings(ids)
	return ids
}

func TestTokenForms(t *testing.T) {
	has := func(forms []string, want string) bool {
		for _, f := range forms {
			if f == want {
				return true
			}
		}
		return false
	}
	forms := appendTokenForms(nil, "والعراق")
	if !has(forms, "والعراق") || !has(forms, "العراق") || !has(forms, "عراق") {
		t.Errorf("forms of والعراق = %q", forms)
	}
	forms = appendTokenForms(nil, "للعراق")
	if !has(forms, "عراق") || !has(forms, "العراق") {
		t.Errorf("forms of للعراق = %q", forms)
	}
	if forms := appendTokenForms(nil, "biraq"); !reflect.DeepEqual(forms, []string{"biraq"}) {
		t.Errorf("Latin token was stripped: %q", forms)
	}
	if forms := appendTokenForms(nil, "ول"); !reflect.DeepEqual(forms, []string{"ول"}) {
		t.Errorf("short token was stripped to a single letter: %q", forms)
	}
}

func TestMatchProclitics(t *testing.T) {
	texts := []string{"العراق", "والعراق", "بالعراق", "للعراق", "كالعراق", "فالعراق"}
	for _, variant := range []string{"العراق", "عراق"} {
		m := NewMatcher([]Keyword{{ID: "k", Variants: []string{variant}}})
		for _, text := range texts {
			hits := m.Match(text)
			want := []Hit{{KeywordID: "k", Variant: variant, Start: 0, End: 1}}
			if !reflect.DeepEqual(hits, want) {
				t.Errorf("variant %q, text %q: hits = %+v, want %+v", variant, text, hits, want)
			}
		}
		// Not a proclitic, two stacked proclitics, or a longer word.
		for _, text := range []string{"مالعراق", "وبالعراق", "العراقي", "عراقيون", "العراقية"} {
			if hits := m.Match(text); len(hits) != 0 {
				t.Errorf("variant %q, text %q: unexpected hits %+v", variant, text, hits)
			}
		}
	}

	// A one-letter prefix on a name that has no article.
	m := NewMatcher([]Keyword{{ID: "baghdad", Variants: []string{"بغداد"}}})
	for _, text := range []string{"بغداد", "وبغداد", "لبغداد", "فبغداد"} {
		if got := hitIDs(m.Match(text)); !reflect.DeepEqual(got, []string{"baghdad"}) {
			t.Errorf("text %q: ids = %q", text, got)
		}
	}
}

func TestMatchMultiWord(t *testing.T) {
	m := NewMatcher([]Keyword{
		{ID: "kurdistan-region", Variants: []string{"إقليم كوردستان", "Kurdistan Region"}},
		{ID: "dhi-qar", Variants: []string{"Dhi Qar", "ذي قار"}},
	})
	tests := []struct {
		text string
		want []Hit
	}{
		{"زيارة إلى إقليم كوردستان اليوم", []Hit{{"kurdistan-region", "إقليم كوردستان", 2, 4}}},
		{"وإقليم كوردستان", []Hit{{"kurdistan-region", "إقليم كوردستان", 0, 2}}},
		{"Life in the Kurdistan Region of Iraq", []Hit{{"kurdistan-region", "Kurdistan Region", 3, 5}}},
		{"Dhi-Qar province", []Hit{{"dhi-qar", "Dhi Qar", 0, 2}}},
		{"DHI\nQAR", []Hit{{"dhi-qar", "Dhi Qar", 0, 2}}},
		{"محافظة ذي قار", []Hit{{"dhi-qar", "ذي قار", 1, 3}}},
		{"بذي قار", []Hit{{"dhi-qar", "ذي قار", 0, 2}}},
		{"Dhi", nil},
		{"Qar Dhi", nil},
		{"Dhi Qari", nil},
		{"إقليم", nil},
		{"Kurdistan", nil},
	}
	for _, tc := range tests {
		if got := m.Match(tc.text); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Match(%q) = %+v, want %+v", tc.text, got, tc.want)
		}
	}
}

func TestMatchWholeWordsOnly(t *testing.T) {
	m := NewMatcher([]Keyword{{ID: "iraq", Variants: []string{"Iraq"}}})
	for _, text := range []string{"Iraq", "IRAQ", "#Iraq", "Iraq's", "news from iraq.", "(Iraq)"} {
		if got := hitIDs(m.Match(text)); !reflect.DeepEqual(got, []string{"iraq"}) {
			t.Errorf("%q: ids = %q, want [iraq]", text, got)
		}
	}
	for _, text := range []string{"Iraqiya", "Iraqi", "SuperIraq", "Iraq2024", "Ira q"} {
		if hits := m.Match(text); len(hits) != 0 {
			t.Errorf("%q: unexpected partial-word hits %+v", text, hits)
		}
	}
	// Listing the longer word makes it match.
	m = NewMatcher([]Keyword{{ID: "iraq", Variants: []string{"Iraq", "Iraqiya"}}})
	if got := hitIDs(m.Match("Iraqiya TV")); !reflect.DeepEqual(got, []string{"iraq"}) {
		t.Errorf("listed variant: ids = %q", got)
	}
}

func TestMatchKurmanjiAndTurkish(t *testing.T) {
	m := NewMatcher([]Keyword{
		{ID: "erbil", Variants: []string{"Hewlêr", "Hewlêrê", "Erbil"}},
		{ID: "kirkuk", Variants: []string{"Kerkûk", "Kerkük"}},
		{ID: "mosul", Variants: []string{"Musul", "Mûsil"}},
	})
	tests := []struct {
		text string
		want []string
	}{
		{"Ez li Hewlêrê dijîm", []string{"erbil"}},
		{"HEWLÊR û KERKÛK", []string{"erbil", "kirkuk"}},
		{"Kerkuk without accents", []string{"kirkuk"}},
		{"Kerkük'te Türkmenler ve Musul", []string{"kirkuk", "mosul"}},
		{"Erbil'de bir gün", []string{"erbil"}},
		{"Mûsil û Hewler", []string{"erbil", "mosul"}},
	}
	for _, tc := range tests {
		if got := hitIDs(m.Match(tc.text)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Match(%q) ids = %q, want %q", tc.text, got, tc.want)
		}
	}
	// The reported variant is the first listed spelling with that normalized form.
	hits := m.Match("KERKÜK")
	if len(hits) != 1 || hits[0].Variant != "Kerkûk" {
		t.Errorf("hits = %+v, want one hit with variant Kerkûk", hits)
	}
}

func TestMatchDedupAndOrder(t *testing.T) {
	// Two variants of one keyword at the same position: reported once, using
	// the longer variant.
	m := NewMatcher([]Keyword{{ID: "iraq", Variants: []string{"عراق", "العراق"}}})
	want := []Hit{{"iraq", "العراق", 0, 1}}
	if got := m.Match("والعراق"); !reflect.DeepEqual(got, want) {
		t.Errorf("same position: %+v, want %+v", got, want)
	}

	// A shorter match of the same keyword inside a longer one is dropped;
	// other keywords inside it are kept.
	m = NewMatcher([]Keyword{
		{ID: "kr", Variants: []string{"كردستان العراق", "إقليم كردستان العراق"}},
		{ID: "iq", Variants: []string{"العراق"}},
	})
	want = []Hit{{"kr", "إقليم كردستان العراق", 1, 4}, {"iq", "العراق", 3, 4}}
	if got := m.Match("في إقليم كردستان العراق"); !reflect.DeepEqual(got, want) {
		t.Errorf("containment: %+v, want %+v", got, want)
	}

	// Same start: longest first, then by ID. Repeats at later positions are
	// reported again.
	m = NewMatcher([]Keyword{
		{ID: "z", Variants: []string{"Basra"}},
		{ID: "y", Variants: []string{"Basra"}},
		{ID: "x", Variants: []string{"Basra Gulf"}},
	})
	want = []Hit{
		{"x", "Basra Gulf", 0, 2}, {"y", "Basra", 0, 1}, {"z", "Basra", 0, 1},
		{"y", "Basra", 3, 4}, {"z", "Basra", 3, 4},
	}
	if got := m.Match("Basra Gulf and Basra"); !reflect.DeepEqual(got, want) {
		t.Errorf("order: %+v, want %+v", got, want)
	}

	// Same output every time.
	first := m.Match("Basra Gulf and Basra")
	for i := 0; i < 20; i++ {
		if got := m.Match("Basra Gulf and Basra"); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d: %+v differs from %+v", i, got, first)
		}
	}
}

func TestMatchEdgeCases(t *testing.T) {
	var nilMatcher *Matcher
	if hits := nilMatcher.Match("Iraq"); hits != nil {
		t.Errorf("nil matcher: %+v", hits)
	}
	if hits := NewMatcher(nil).Match("Iraq"); hits != nil {
		t.Errorf("empty matcher: %+v", hits)
	}
	m := NewMatcher([]Keyword{{ID: "k", Variants: []string{"", "!!!", "Iraq"}}})
	if hits := m.Match(""); hits != nil {
		t.Errorf("empty text: %+v", hits)
	}
	if got := hitIDs(m.Match("iraq")); !reflect.DeepEqual(got, []string{"k"}) {
		t.Errorf("empty variants should be skipped, not break matching: %q", got)
	}
}

func TestMatchConcurrent(t *testing.T) {
	kws, err := Seed()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(kws)
	texts := []string{
		"انفجار في بغداد والبصرة اليوم",
		"هەولێر و سلێمانی و دهۆک",
		"Ez ji Hewlêrê me û diçim Duhokê",
		"Mosul, Erbil & Kirkuk travel vlog",
		"Irak'ın başkenti Bağdat",
	}
	want := make([][]Hit, len(texts))
	for i, text := range texts {
		want[i] = m.Match(text)
	}
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				i := n % len(texts)
				if got := m.Match(texts[i]); !reflect.DeepEqual(got, want[i]) {
					t.Errorf("concurrent Match(%q) = %+v, want %+v", texts[i], got, want[i])
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestParseTSV(t *testing.T) {
	good := "# comment\n\n  # indented comment\r\n" +
		"iraq\tiraq\tplace\ten\t1\t Iraq | Irak ||\r\n"
	kws, err := parseTSV("test", good)
	if err != nil {
		t.Fatal(err)
	}
	want := []Keyword{{ID: "iraq", Group: "iraq", Kind: "place", Lang: "en", Variants: []string{"Iraq", "Irak"}, Ambiguous: true}}
	if !reflect.DeepEqual(kws, want) {
		t.Errorf("parseTSV = %+v, want %+v", kws, want)
	}

	bad := map[string]string{
		"too few columns":   "a\tb\tc\td\t0\n",
		"too many columns":  "a\tb\tc\td\t0\tx\ty\n",
		"bad ambiguous":     "a\tb\tc\td\t2\tx\n",
		"empty group":       "a\t\tc\td\t0\tx\n",
		"no variants":       "a\tb\tc\td\t0\t | \n",
		"variant no letter": "a\tb\tc\td\t0\t!!!\n",
		"duplicate id":      "a\tb\tc\td\t0\tx\na\tb\tc\td\t0\ty\n",
	}
	for name, data := range bad {
		if _, err := parseTSV("test", data); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// seedGroups are the groups the seed must cover: the country, the Kurdistan
// Region and the governorates (18 plus Halabja).
var seedGroups = []string{
	"iraq", "kurdistan-region",
	"baghdad", "basra", "nineveh", "erbil", "sulaymaniyah", "duhok", "halabja", "kirkuk", "diyala",
	"anbar", "babil", "karbala", "najaf", "wasit", "maysan", "dhi-qar", "muthanna", "qadisiyyah", "saladin",
}

func TestSeedFile(t *testing.T) {
	kws, err := Seed()
	if err != nil {
		t.Fatal(err)
	}
	if len(kws) < len(seedGroups) {
		t.Fatalf("only %d seed keywords", len(kws))
	}
	allowedLang := map[string]bool{"ar": true, "ckb": true, "kmr": true, "en": true, "tr": true}
	ids := map[string]bool{}
	groups := map[string]bool{}
	clearGroups := map[string]bool{} // groups with at least one unambiguous keyword
	normOwner := map[string]string{} // normalized variant -> keyword ID
	for _, kw := range kws {
		if ids[kw.ID] {
			t.Errorf("duplicate id %q", kw.ID)
		}
		ids[kw.ID] = true
		groups[kw.Group] = true
		if !kw.Ambiguous {
			clearGroups[kw.Group] = true
		}
		if kw.Kind != "place" {
			t.Errorf("%s: kind %q, want place", kw.ID, kw.Kind)
		}
		if len(kw.Variants) == 0 {
			t.Errorf("%s: no variants", kw.ID)
		}

		// Every listed language is known, and the variants include the
		// script that language is written in.
		var hasArabicScript, hasLatin bool
		for _, v := range kw.Variants {
			for _, r := range v {
				if unicode.Is(unicode.Arabic, r) {
					hasArabicScript = true
				}
				if unicode.Is(unicode.Latin, r) {
					hasLatin = true
				}
			}
		}
		for _, lang := range strings.Split(kw.Lang, ",") {
			if !allowedLang[lang] {
				t.Errorf("%s: unknown language %q", kw.ID, lang)
			}
			if (lang == "ar" || lang == "ckb") && !hasArabicScript {
				t.Errorf("%s: lists %s but has no Arabic-script variant", kw.ID, lang)
			}
			if (lang == "kmr" || lang == "en" || lang == "tr") && !hasLatin {
				t.Errorf("%s: lists %s but has no Latin variant", kw.ID, lang)
			}
		}

		// No two keywords share a normalized spelling, or one text would be
		// reported twice.
		for _, v := range kw.Variants {
			n := Normalize(v)
			if owner, ok := normOwner[n]; ok && owner != kw.ID {
				t.Errorf("variant %q (%q) is in both %s and %s", v, n, owner, kw.ID)
			}
			normOwner[n] = kw.ID
		}
	}
	for _, g := range seedGroups {
		if !groups[g] {
			t.Errorf("missing group %q", g)
		}
		if !clearGroups[g] {
			t.Errorf("group %q has no unambiguous keyword", g)
		}
	}
	if len(groups) != len(seedGroups) {
		t.Errorf("seed has %d groups, want %d", len(groups), len(seedGroups))
	}

	byID := map[string]Keyword{}
	for _, kw := range kws {
		byID[kw.ID] = kw
	}
	for _, id := range []string{"wasit", "muthanna", "saladin", "babil", "qadisiyyah", "kut", "amarah", "baghdad-ambiguous"} {
		if !byID[id].Ambiguous {
			t.Errorf("%s should be ambiguous", id)
		}
	}
	for _, id := range []string{"iraq", "iraqi", "kurdistan-region", "baghdad", "erbil", "kirkuk", "babil-governorate"} {
		if kw, ok := byID[id]; !ok || kw.Ambiguous {
			t.Errorf("%s should exist and be unambiguous", id)
		}
	}
}

func TestSeedSamples(t *testing.T) {
	kws, err := Seed()
	if err != nil {
		t.Fatal(err)
	}
	m := NewMatcher(kws)
	tests := []struct {
		lang, text string
		want       []string
	}{
		// Arabic.
		{"ar", "انفجار في بغداد والبصرة اليوم", []string{"baghdad", "basra"}},
		{"ar", "أخبار العراق: محافظة صلاح الدين وتكريت", []string{"iraq", "saladin", "saladin-governorate", "tikrit"}},
		{"ar", "للعراقيين في إقليم كردستان العراق", []string{"iraq", "iraqi", "kurdistan-region"}},
		{"ar", "#البصرة #الموصل #كربلاء_المقدسة", []string{"basra", "karbala", "mosul"}},
		{"ar", "مُحافَظَةُ نِينَوَى وَالأَنْبَار", []string{"anbar", "nineveh"}},
		{"ar", "حكم بعقوبة السجن", []string{"baqubah-ambiguous"}},

		// Kurdish Sorani.
		{"ckb", "هەولێر و سلێمانی و دهۆک", []string{"duhok", "erbil", "sulaymaniyah-ambiguous"}},
		{"ckb", "پارێزگای سلێمانی", []string{"sulaymaniyah", "sulaymaniyah-ambiguous"}},
		{"ckb", "هەرێمی کوردستان و بەغدا", []string{"baghdad", "kurdistan-region"}},
		{"ckb", "کەرکووک و هەڵەبجە", []string{"halabja", "kirkuk"}},

		// Kurdish Kurmanji.
		{"kmr", "Ez ji Hewlêrê me û diçim Duhokê", []string{"duhok", "erbil"}},
		{"kmr", "Herêma Kurdistanê û Bexda", []string{"baghdad", "kurdistan-region"}},
		{"kmr", "Kerkûk, Mûsil û Silêmanî", []string{"kirkuk", "mosul", "sulaymaniyah-ambiguous"}},

		// English.
		{"en", "Iraqi farmers in Dhi Qar and Nasiriyah", []string{"dhi-qar", "iraqi", "nasiriyah"}},
		{"en", "Mosul, Erbil & Kirkuk travel vlog", []string{"erbil", "kirkuk", "mosul"}},
		{"en", "Life in the Kurdistan Region of Iraq", []string{"iraq", "kurdistan-region"}},
		{"en", "Fallujah and Ramadi, Anbar province", []string{"anbar", "fallujah", "ramadi"}},
		{"en", "Iraqiya TV and SuperIraq", []string{}},

		// Turkish and Turkmen.
		{"tr", "Kerkük'te Türkmen kültürü ve Musul", []string{"kirkuk", "mosul"}},
		{"tr", "Irak'ın başkenti Bağdat", []string{"baghdad-ambiguous", "iraq"}},
		{"tr", "Iraklı gençler Erbil'de", []string{"erbil", "iraqi"}},
		{"tr", "Telafer ve Halepçe", []string{"halabja"}},
	}
	for _, tc := range tests {
		got := hitIDs(m.Match(tc.text))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("[%s] %q: ids = %q, want %q", tc.lang, tc.text, got, tc.want)
		}
	}
}

func BenchmarkMatchSeed(b *testing.B) {
	kws, err := Seed()
	if err != nil {
		b.Fatal(err)
	}
	m := NewMatcher(kws)
	text := strings.Repeat("أخبار العراق اليوم من بغداد والبصرة، Kerkük'te hayat, Mosul travel vlog هەولێر #Iraq ", 10)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Match(text)
	}
}

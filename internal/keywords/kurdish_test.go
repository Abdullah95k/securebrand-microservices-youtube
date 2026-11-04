package keywords

import "testing"

func TestKurdish(t *testing.T) {
	for _, s := range []string{
		"ئامێدی   گازندەیا وەلاتیان",
		"ئەمڕۆ لە سلێمانی، لە نۆرەی وەرگرتنی بەنزیندا شۆفێرێک ئۆتۆمبێل لێیدا و گیانی لەدەستدا",
		"ڕۆژی 3 هێلکە بخۆی چی ڕودەدات ..؟؟ زۆر گرنگە بیزانی ..!!",
		"پێشمه‌رگه‌ له‌ته‌واوی میحوه‌ره‌كان له‌ ئاماده‌باشیدایه‌", // older spelling: ه‌ for ە
		"هەواڵی ئەمڕۆ",
	} {
		if !Kurdish(s) {
			t.Errorf("Kurdish(%q) = false", s)
		}
	}
	for _, s := range []string{
		"الگيمر والكاهي 😂🥐 | #العراق #كوميديا",                     // Iraqi Arabic with گ
		"بعد 15 عاماً من الإغلاق.. مسجد التكية السليمانية في دمشق", // Arabic
		"این یک ویدیو درباره تهران است",                            // Persian
		"ئۇيغۇر تىلى ۋە مەدەنىيىتى",                                // Uyghur
		"Kurdistan Region news", // Latin script
		"",
	} {
		if Kurdish(s) {
			t.Errorf("Kurdish(%q) = true", s)
		}
	}
}

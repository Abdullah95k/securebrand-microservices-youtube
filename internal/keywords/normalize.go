package keywords

import (
	"strings"
	"unicode"
)

// Normalize turns text into the form used for keyword matching. The result
// is lossy and meant for matching only; never store or show it in place of
// the original text.
//
// What it does:
//   - Arabic: alef variants (أ إ آ ٱ) become ا; ة becomes ه; ى and ئ become ي;
//     ؤ becomes و; tatweel (ـ) and harakat/diacritics are removed.
//   - Persian and Kurdish (Sorani) letters fold to their Arabic forms:
//     ک→ك, ی→ي, ێ→ي, ۆ→و, ە→ه, ڵ→ل, ڕ→ر, and the alternative heh forms
//     ھ ۀ ہ → ه. Letters with no Arabic equivalent, such as گ پ چ ژ ڤ, stay.
//   - Eastern Arabic (٠-٩) and Persian (۰-۹) digits become ASCII digits.
//   - Latin: lowercased, and accented letters fold to plain ones
//     (Kurmanji ê î û ç ş, Turkish ı ğ ş ç ö ü, French/Portuguese é è â and
//     so on, plus macron and dot-below transliteration letters). Combining
//     accent marks (decomposed text) are removed.
//   - Invisible format characters (zero-width joiners, direction marks) are
//     removed, so a word broken by one stays one word.
//   - Punctuation and symbols, including Arabic ، ؛ ؟, underscores and emoji,
//     become spaces. This also drops the "#" from hashtags and splits
//     "#بغداد_اليوم" into its words.
//   - Runs of whitespace collapse to one space, with none at either end.
func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false // a separator was seen since the last word character
	for _, r := range s {
		r = foldRune(unicode.ToLower(r))
		switch {
		case r == dropRune:
			continue
		case unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
		default:
			// Whitespace, punctuation, symbols and control characters.
			pendingSpace = true
		}
	}
	return b.String()
}

// dropRune is returned by foldRune for characters that are removed.
const dropRune rune = -1

// foldRune maps one already-lowercased rune to its matching form. It returns
// dropRune for characters to remove and ' ' for characters that separate
// words but are not classed as punctuation by Unicode.
func foldRune(r rune) rune {
	switch {
	case r < 0x80:
		return r // ASCII is classified by the caller
	case r >= 0x0660 && r <= 0x0669: // Eastern Arabic digits
		return '0' + (r - 0x0660)
	case r >= 0x06F0 && r <= 0x06F9: // Persian digits
		return '0' + (r - 0x06F0)
	case r == 0x0640: // tatweel
		return dropRune
	case r >= 0x064B && r <= 0x065F, r == 0x0670: // harakat, superscript alef
		return dropRune
	case r >= 0x0610 && r <= 0x061A, r >= 0x06D6 && r <= 0x06ED, r >= 0x08D3 && r <= 0x08FF:
		// Other Arabic marks (honorifics, Quranic annotation).
		if unicode.Is(unicode.Mn, r) {
			return dropRune
		}
	case r >= 0x0300 && r <= 0x036F: // combining accents on Latin letters
		return dropRune
	case r == 0x02BE || r == 0x02BF: // ʾ ʿ hamza/ayn in transliterations ("Baʿqubah")
		return dropRune
	case r == 0x02BC: // ʼ modifier apostrophe: treat like an ASCII apostrophe
		return ' '
	case r == 0x200B: // zero-width space separates words
		return ' '
	}
	if m, ok := foldTable[r]; ok {
		return m
	}
	if unicode.Is(unicode.Cf, r) {
		// Zero-width (non-)joiners, direction marks, BOM, soft hyphen.
		return dropRune
	}
	return r
}

// foldTable maps single letters to their matching form. Keys are lowercase.
var foldTable = map[rune]rune{
	// Arabic letter variants.
	'أ': 'ا', 'إ': 'ا', 'آ': 'ا', 'ٱ': 'ا',
	'ة': 'ه',
	'ى': 'ي',
	'ؤ': 'و',
	'ئ': 'ي',

	// Persian and Kurdish (Sorani) letters.
	'ک': 'ك', // keheh
	'ی': 'ي', // Farsi yeh
	'ێ': 'ي', // yeh with small v (Sorani ê)
	'ۆ': 'و', // waw with small v (Sorani o)
	'ە': 'ه', // ae (Sorani e)
	'ڵ': 'ل', // lam with small v (Sorani heavy l)
	'ڕ': 'ر', // reh with small v (Sorani trilled r)
	'ھ': 'ه', // heh doachashmee, often typed for Kurdish h
	'ۀ': 'ه', // heh with yeh above
	'ہ': 'ه', // heh goal

	// Latin letters with accents.
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a', 'ā': 'a',
	'ç': 'c',
	'ḍ': 'd', 'ḏ': 'd',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e',
	'ğ': 'g',
	'ḥ': 'h',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i', 'ı': 'i',
	'ñ': 'n',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ō': 'o',
	'ş': 's', 'ș': 's', 'ṣ': 's',
	'ţ': 't', 'ț': 't', 'ṭ': 't', 'ṯ': 't',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u', 'ū': 'u',
	'ý': 'y', 'ÿ': 'y',
	'ẓ': 'z',
}

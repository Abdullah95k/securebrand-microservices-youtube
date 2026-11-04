package keywords

// KurdishID is the match ID reported for text written in Kurdish.
const KurdishID = "lang-ckb"

// Kurdish reports whether text is written in Kurdish in Arabic script:
// Sorani or Badini, the forms used in Iraq. Such content counts as relevant
// on its own (D28, 5 November 2026).
//
// It looks for Kurdish's own letters, which Arabic and Persian never use:
// one of ڕ ڵ ێ, or two of ە ۆ. Uyghur also writes ە and ۆ, but it has
// letters Kurdish does not (ۇ ۈ ې), so text with those is left out.
// Normalize folds these letters away, so this runs on the original text.
func Kurdish(text string) bool {
	strong, weak := 0, 0
	for _, r := range text {
		switch r {
		case 'ڕ', 'ڵ', 'ێ':
			strong++
		case 'ە', 'ۆ':
			weak++
		case 'ۇ', 'ۈ', 'ې':
			return false
		}
	}
	return strong > 0 || weak >= 2
}

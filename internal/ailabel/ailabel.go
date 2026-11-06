// Package ailabel holds the types of the Phase 0 AI labelling (E11): the
// titles sent, the verdicts returned and the possible answers. The client
// that calls the AI provider's API is in the subpackage claude, which only
// ytprobe imports: the production collector (ytcollect) never links the AI
// SDK (REQ-010 fix 9).
//
// Only the titles leave the server: no video or channel IDs, no comments,
// no commenter names, and not the keyword matcher's own decision (the AI
// labels blind, like a person would). The API key is read by the caller
// from a file and passed in; it is never logged.
package ailabel

import "errors"

// Answers the model can give for one title.
const (
	Yes    = "yes"
	No     = "no"
	Unsure = "unsure"
)

// ErrAnswer marks a reply that could not be used (cut off or not the
// expected JSON). Asking again, with fewer titles, usually works.
var ErrAnswer = errors.New("ailabel: unusable answer")

// Title is one title to label; ID is local to the request.
type Title struct {
	ID   int    `json:"id"`
	Text string `json:"title"`
}

// Verdict is the model's answer for one title.
type Verdict struct {
	ID     int    `json:"id"`
	Answer string `json:"answer"` // Yes, No or Unsure
	Reason string `json:"reason"`
}

// Result is one request's outcome.
type Result struct {
	Verdicts     []Verdict
	Model        string // the model that produced the answers
	Refused      bool   // the model (and any fallback) declined
	InputTokens  int64
	OutputTokens int64
}

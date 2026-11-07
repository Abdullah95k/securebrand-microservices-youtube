package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	coveragev1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/coverage/v1"
	discoveryv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/discovery/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The contract's discovery and coverage topics.
const (
	TopicCandidate = "discovery.candidate.v1"
	TopicCoverage  = "collection.coverage.v1"
)

// apiTermsAge is how long the official Data API's data may be kept (its
// terms; discovery.APIDataMaxAge).
const apiTermsAge = 30 * 24 * time.Hour

type candidateRow struct {
	ChannelID  string     `json:"channel_id"`
	FirstFound time.Time  `json:"first_found"`
	Sources    []string   `json:"sources"`
	Status     string     `json:"status"`
	CheckedAt  *time.Time `json:"checked_at"`
	Decision   *string    `json:"decision"`
	Channel    *struct {
		Kind   string `json:"kind"`
		Status string `json:"status"`
	} `json:"channel"`
	Evidence []struct {
		Engine      string     `json:"engine"`
		Keyword     *string    `json:"keyword"`
		VideoID     *string    `json:"video_id"`
		PublishedAt *time.Time `json:"published_at"`
	} `json:"evidence"`
}

// provenance is the row's provenance, for messages without an envelope.
func (b *builder) provenance(acq contentv1.Acquisition) *contentv1.Provenance {
	return &contentv1.Provenance{Collector: b.src.Collector, CollectorVersion: b.src.CollectorVersion,
		ParserVersion: b.src.ParserVersion, SourceOperation: first(b.src.Operations), Acquisition: acq,
		Mode: crawlMode(b.src.JobKind), RawRef: first(b.src.RawRefs), ExitCountry: first(b.src.ExitCountries),
		JobIds: nonEmpty(b.src.JobID)}
}

func eventID(kind, id string, version int64) string {
	h := sha256.Sum256([]byte(kind + "|" + id + "|" + strconv.FormatInt(version, 10)))
	return hex.EncodeToString(h[:])
}

// candidateSource is how discovery found it (its first source).
func candidateSource(sources []string) discoveryv1.Source {
	if len(sources) == 0 {
		return discoveryv1.Source_SOURCE_RELATED
	}
	switch sources[0] {
	case "web_search":
		return discoveryv1.Source_SOURCE_KEYWORD_SEARCH
	case "api_search":
		return discoveryv1.Source_SOURCE_OFFICIAL_API_SEARCH
	case "commenter":
		return discoveryv1.Source_SOURCE_COMMENTER
	}
	return discoveryv1.Source_SOURCE_RELATED
}

// candidate: discovery.candidate, a channel discovery found, with its
// outcome once checked. An official-API hit gives its keyword only (the
// YouTube terms): no video, no API field.
func (b *builder) candidate() ([]Message, error) {
	var c candidateRow
	if err := b.decode(&c); err != nil {
		return nil, err
	}
	id := accountID(c.ChannelID)
	apiOnly := len(c.Sources) > 0 && !slices.ContainsFunc(c.Sources, func(s string) bool { return s != "api_search" })
	acq := contentv1.Acquisition_ACQUISITION_SCRAPED_LOGGED_OUT
	if apiOnly {
		acq = contentv1.Acquisition_ACQUISITION_OFFICIAL_API
	}
	m := &discoveryv1.Candidate{
		EventId: eventID("candidate", id, b.row.ObservedAt.UnixMicro()), TargetPlatform: Platform,
		Account: &contentv1.AccountRef{Id: id, NativeId: c.ChannelID, Url: channelURL(c.ChannelID), Type: contentv1.AccountType_ACCOUNT_TYPE_CHANNEL},
		FoundAt: timestamppb.New(c.FirstFound), FoundBy: "yts", Source: candidateSource(c.Sources),
		Provenance: b.provenance(acq),
	}
	if apiOnly {
		m.Provenance.TermsExpireAt = timestamppb.New(c.FirstFound.Add(apiTermsAge))
	}
	day := 86400
	for _, e := range c.Evidence {
		ev := &discoveryv1.Evidence{Keyword: deref(e.Keyword), Via: e.Engine + "_search"}
		if e.Engine == "web" && e.VideoID != nil {
			ev.ContentId, ev.Url = postID(*e.VideoID), videoURL(*e.VideoID, "video")
			ev.ContentTime = estimate(e.PublishedAt, &day, nil)
		}
		m.Evidence = append(m.Evidence, ev)
	}
	switch c.Status {
	case "listed":
		switch {
		case c.Decision == nil || *c.Decision == "":
			m.Outcome = discoveryv1.Outcome_OUTCOME_ALREADY_LISTED // listed before discovery checked it
		case c.Channel != nil && c.Channel.Kind == "mention":
			m.Outcome, m.Scope = discoveryv1.Outcome_OUTCOME_ADDED_RELEVANT_ONLY, contentv1.CollectionScope_COLLECTION_SCOPE_RELEVANT_ONLY
		default:
			m.Outcome, m.Scope = discoveryv1.Outcome_OUTCOME_ADDED, contentv1.CollectionScope_COLLECTION_SCOPE_FULL
		}
	case "no_match":
		if c.Decision != nil && *c.Decision == "channel not found" {
			m.Outcome, m.OutcomeReason = discoveryv1.Outcome_OUTCOME_UNAVAILABLE, "gone"
		} else {
			m.Outcome, m.OutcomeReason = discoveryv1.Outcome_OUTCOME_NOT_RELEVANT, "not_relevant"
		}
	default: // new, checked
		m.Outcome = discoveryv1.Outcome_OUTCOME_PENDING
	}
	if c.CheckedAt != nil && m.Outcome != discoveryv1.Outcome_OUTCOME_PENDING {
		m.DecidedAt = timestamppb.New(*c.CheckedAt)
	}
	return []Message{{Topic: TopicCandidate, Key: id, Msg: m}}, nil
}

type coverageRow struct {
	Hour     time.Time `json:"hour"`
	Kind     string    `json:"kind"`
	Jobs     int64     `json:"jobs"`
	Failures int64     `json:"failures"`
	Requests int64     `json:"requests"`
	Bytes    int64     `json:"bytes"`
	Blocks   int64     `json:"blocks"`
	RunMs    int64     `json:"run_ms"`
}

// jobKind is the contract's kind of a yts job kind.
func jobKind(k string) coveragev1.JobKind {
	switch k {
	case "backfill":
		return coveragev1.JobKind_JOB_KIND_BACKFILL
	case "candidate":
		return coveragev1.JobKind_JOB_KIND_DISCOVERY_CHECK
	}
	return coveragev1.JobKind_JOB_KIND_SCHEDULED
}

// coverage: collection.coverage, an hour's roll-up of one job kind:
// job_count jobs, their requests, bytes and blocks, and how many failed.
func (b *builder) coverage() ([]Message, error) {
	var c coverageRow
	if err := b.decode(&c); err != nil {
		return nil, err
	}
	jobID := "yts:" + b.row.EntityID
	m := &coveragev1.JobCoverage{
		EventId: eventID("coverage", jobID, b.row.ObservedAt.UnixMicro()), Platform: Platform, Collector: "yts", JobId: jobID,
		Kind: jobKind(c.Kind), CreatedAt: timestamppb.New(c.Hour), FinishedAt: timestamppb.New(c.Hour.Add(time.Hour)),
		WindowFrom: timestamppb.New(c.Hour), WindowTo: timestamppb.New(c.Hour.Add(time.Hour)),
		Counters: map[string]int64{"requests": c.Requests, "bytes": c.Bytes, "blocked": c.Blocks,
			"yt.jobs": c.Jobs, "yt.failures": c.Failures, "yt.run_ms": c.RunMs},
		Provenance: b.provenance(contentv1.Acquisition_ACQUISITION_DERIVED), JobCount: c.Jobs,
	}
	m.Provenance.SourceOperation = "rollup:" + c.Kind
	switch {
	case c.Jobs > 0 && c.Failures >= c.Jobs:
		m.Status = coveragev1.JobStatus_JOB_STATUS_FAILED
	case c.Failures > 0:
		m.Status = coveragev1.JobStatus_JOB_STATUS_PARTIAL
	default:
		m.Status = coveragev1.JobStatus_JOB_STATUS_COMPLETED
	}
	if c.Blocks > 0 {
		m.Reasons = append(m.Reasons, &coveragev1.Reason{Class: "rate_limited", Count: c.Blocks, Detail: "answers YouTube refused (blocks)"})
	}
	if c.Failures > 0 {
		m.Reasons = append(m.Reasons, &coveragev1.Reason{Class: "failed", Count: c.Failures,
			Detail: strconv.FormatInt(c.Failures, 10) + " of " + strconv.FormatInt(c.Jobs, 10) + " jobs failed"})
	}
	return []Message{{Topic: TopicCoverage, Key: jobID, Msg: m}}, nil
}

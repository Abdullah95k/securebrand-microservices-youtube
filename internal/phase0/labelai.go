package phase0

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ailabel"
)

// TitleLabeler is the AI (ailabel.Client; a fake in tests).
type TitleLabeler interface {
	Label(ctx context.Context, titles []ailabel.Title) (ailabel.Result, error)
}

// AILabelOptions configures an AI labelling run.
type AILabelOptions struct {
	LabelOptions
	Batch int // titles per request
}

// LabelAI has the AI label every sample title it has not labelled yet. Only
// the titles are sent: no IDs and not the matcher's decision. Every answer
// is saved at once, so a stopped run continues where it ended. Titles the
// AI left out, or whose reply was unusable, are asked again in smaller
// batches; titles it declines to judge count as "can't tell".
func LabelAI(ctx context.Context, s *Store, ai TitleLabeler, o AILabelOptions, log *slog.Logger) error {
	if o.Size <= 0 {
		o.Size = 300
	}
	if o.Batch <= 0 {
		o.Batch = 25
	}
	sample, err := labelSample(s, o.LabelOptions)
	if err != nil {
		return err
	}
	answers, err := ReadAll[LabelAnswer](s, "labels")
	if err != nil {
		return err
	}
	done := map[int]bool{}
	for _, a := range answers {
		if a.Source == SourceAI {
			done[a.Index] = true
		}
	}
	var pending []LabelItem
	for _, it := range sample {
		if !done[it.Index] {
			pending = append(pending, it)
		}
	}
	before := len(sample) - len(pending)
	log.Info("AI labelling", "sample", len(sample), "already_labelled", before, "to_label", len(pending))
	var st aiRunStats
	// Pass 1 uses full batches; pass 2 retries what is missing in batches
	// of 5, where a declined batch is recorded as "can't tell".
	for pass, size := range []int{o.Batch, min(o.Batch, 5)} {
		if len(pending) == 0 {
			break
		}
		final := pass == 1
		var missed []LabelItem
		for start := 0; start < len(pending); start += size {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			batch := pending[start:min(start+size, len(pending))]
			left, err := labelBatch(ctx, s, ai, batch, final, &st)
			if err != nil {
				log.Warn("AI labelling stopped; run the same command to continue", "labelled", st.labelled, "err", err)
				return err
			}
			missed = append(missed, left...)
			log.Info("AI labelling progress", "labelled", before+st.labelled, "of", len(sample))
		}
		pending = missed
	}
	log.Info("AI labelling done", "labelled_now", st.labelled, "cant_tell", st.unsure, "declined", st.declined,
		"requests", st.requests, "input_tokens", st.in, "output_tokens", st.out)
	if len(pending) > 0 {
		log.Warn("some titles got no usable answer; run the same command again to retry them", "titles", len(pending))
	}
	return nil
}

type aiRunStats struct {
	requests, labelled, unsure, declined int
	in, out                              int64
}

// labelBatch asks once for batch and saves the answers. It returns the
// items still without an answer, or an error that should stop the run
// (key, model, billing or a lasting API failure).
func labelBatch(ctx context.Context, s *Store, ai TitleLabeler, batch []LabelItem, final bool, st *aiRunStats) ([]LabelItem, error) {
	titles := make([]ailabel.Title, len(batch))
	for i, it := range batch {
		titles[i] = ailabel.Title{ID: i + 1, Text: it.Title}
	}
	res, err := ai.Label(ctx, titles)
	st.requests++
	st.in += res.InputTokens
	st.out += res.OutputTokens
	if errors.Is(err, ailabel.ErrAnswer) {
		return batch, nil // ask again
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if res.Refused {
		if !final {
			return batch, nil // smaller batches next pass
		}
		for _, it := range batch {
			a := LabelAnswer{At: now, Index: it.Index, Skipped: true, Source: SourceAI, Model: res.Model, Reason: "the AI declined to judge this batch"}
			if err := s.Append("labels", a); err != nil {
				return nil, err
			}
			st.labelled++
			st.declined++
		}
		return nil, nil
	}
	got := map[int]bool{}
	for _, v := range res.Verdicts {
		if v.ID < 1 || v.ID > len(batch) {
			continue
		}
		a := LabelAnswer{At: now, Index: batch[v.ID-1].Index, Relevant: v.Answer == ailabel.Yes, Skipped: v.Answer == ailabel.Unsure,
			Source: SourceAI, Model: res.Model, Reason: v.Reason}
		if err := s.Append("labels", a); err != nil {
			return nil, fmt.Errorf("saving an answer: %w", err)
		}
		got[v.ID] = true
		st.labelled++
		if a.Skipped {
			st.unsure++
		}
	}
	var left []LabelItem
	for i, it := range batch {
		if !got[i+1] {
			left = append(left, it)
		}
	}
	return left, nil
}

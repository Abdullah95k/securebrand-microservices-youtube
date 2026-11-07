package provenance_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/provenance"
)

func TestTrail(t *testing.T) {
	ctx, tr := provenance.With(context.Background(), provenance.Job{ID: "channel/UCfake/1", Kind: "channel"})
	if provenance.From(ctx) != tr {
		t.Fatal("From must return the context's trail")
	}
	tr.Record("browse", "s3://raw/yts/30d/1.json.gz", "IQ")
	tr.Record("browse", "s3://raw/yts/30d/2.json.gz", "DE")
	tr.Record("next", "", "IQ")
	tr.Record("", "", "")
	s := tr.Take()
	if s.JobID != "channel/UCfake/1" || s.JobKind != "channel" {
		t.Errorf("job: %+v", s)
	}
	if !slices.Equal(s.Operations, []string{"browse", "next"}) || !slices.Equal(s.ExitCountries, []string{"IQ", "DE"}) || len(s.RawRefs) != 2 {
		t.Errorf("read: %+v", s)
	}
	// Another write from the same answers (nothing read since): the same.
	if s := tr.Take(); s.JobID != "channel/UCfake/1" || !slices.Equal(s.Operations, []string{"browse", "next"}) || len(s.RawRefs) != 2 {
		t.Errorf("a second write from the same answers: %+v", s)
	}
	// A new read: only it.
	tr.Record("player", "", "IQ")
	if s := tr.Take(); !slices.Equal(s.Operations, []string{"player"}) || s.RawRefs != nil {
		t.Errorf("after a new read: %+v", s)
	}
}

func TestNoTrail(t *testing.T) {
	tr := provenance.From(context.Background())
	tr.Record("browse", "s3://x", "IQ") // a nil trail ignores it
	if s := tr.Take(); s.JobID != "" || s.Operations != nil {
		t.Errorf("no trail: %+v", s)
	}
}

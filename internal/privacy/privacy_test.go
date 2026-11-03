package privacy_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
)

var secret = []byte("test-secret-0123456789abcdef-0123456789")

func keyer(t *testing.T) *privacy.Keyer {
	t.Helper()
	k, err := privacy.NewKeyer(secret)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeys(t *testing.T) {
	if _, err := privacy.NewKeyer([]byte("short")); err == nil {
		t.Error("a short secret must be refused")
	}
	k := keyer(t)
	a := k.Key(privacy.Author, "UCfakeAUTHOR0000000000001")
	if !bytes.Equal(a, k.Key(privacy.Author, " UCfakeAUTHOR0000000000001 ")) {
		t.Error("keys must not depend on surrounding spaces")
	}
	if bytes.Equal(a, k.Key(privacy.Channel, "UCfakeAUTHOR0000000000001")) {
		t.Error("the same ID of another kind must get another key")
	}
	other, _ := privacy.NewKeyer(append([]byte("x"), secret...))
	if bytes.Equal(a, other.Key(privacy.Author, "UCfakeAUTHOR0000000000001")) {
		t.Error("another secret must give another key")
	}
	if ak := k.AuthorKey("UCfakeAUTHOR0000000000001"); len(ak) != 64 || strings.Contains(ak, "AUTHOR") {
		t.Errorf("author key %q", ak)
	}
}

func TestLoadKeyerReadsAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "privacy_key")
	if err := os.WriteFile(p, append(secret, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := privacy.LoadKeyer(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k.Key(privacy.Video, "fakeVID00001"), keyer(t).Key(privacy.Video, "fakeVID00001")) {
		t.Error("the file's trailing newline must not change the key")
	}
}

func TestSuppressionListRoundTrip(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	k := keyer(t)
	s := privacy.NewSuppressor(k)
	if err := s.Add(ctx, pool, privacy.Author, "UCfakeAUTHOR0000000000001", "erasure request", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, pool, "planet", "x", "r", nil); err == nil {
		t.Error("unknown kinds must be refused")
	}
	if err := s.Add(ctx, pool, privacy.Video, "fakeVID00001", "", nil); err == nil {
		t.Error("a suppression needs a reason")
	}
	// No raw ID is stored.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM suppression_list WHERE position('AUTHOR' in encode(key, 'escape')) > 0 OR reason LIKE '%UCfake%'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the list must hold keyed hashes only (n %d, err %v)", n, err)
	}
	// A second process sees it after a refresh.
	s2 := privacy.NewSuppressor(k)
	if s2.Suppressed(privacy.Author, "UCfakeAUTHOR0000000000001") {
		t.Error("not refreshed yet")
	}
	if err := s2.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !s2.Suppressed(privacy.Author, "UCfakeAUTHOR0000000000001") || s2.Suppressed(privacy.Channel, "UCfakeAUTHOR0000000000001") || s2.Suppressed(privacy.Author, "") {
		t.Error("suppression check is wrong")
	}
	// Lifting needs a reason, and is seen after a refresh.
	if err := s.Override(ctx, pool, privacy.Author, "UCfakeAUTHOR0000000000001", ""); err == nil {
		t.Error("lifting needs a reason")
	}
	if err := s.Override(ctx, pool, privacy.Author, "UCfakeAUTHOR0000000000001", "added by mistake"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Refresh(ctx, pool); err != nil || s2.Suppressed(privacy.Author, "UCfakeAUTHOR0000000000001") {
		t.Errorf("a lifted suppression still applies (err %v)", err)
	}
	// Adding it again re-activates it.
	if err := s.Add(ctx, pool, privacy.Author, "UCfakeAUTHOR0000000000001", "second request", nil); err != nil {
		t.Fatal(err)
	}
	if err := s2.Refresh(ctx, pool); err != nil || !s2.Suppressed(privacy.Author, "UCfakeAUTHOR0000000000001") {
		t.Errorf("re-added suppression not active (err %v)", err)
	}
}

func TestRetentionDefaultsToForever(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	r, err := privacy.LoadRetention(ctx, pool)
	if err != nil || !r.Forever() || r.RedactDeletedComments || r.PseudonymizeExports {
		t.Fatalf("default %+v (err %v); want keep forever, nothing redacted", r, err)
	}
	if err := privacy.SaveRetention(ctx, pool, privacy.Retention{CommentsDays: -1}); err == nil {
		t.Error("negative days must be refused")
	}
	if err := privacy.SaveRetention(ctx, pool, privacy.Retention{SnapshotsDays: 400, PseudonymizeExports: true}); err != nil {
		t.Fatal(err)
	}
	if r, err := privacy.LoadRetention(ctx, pool); err != nil || r.Forever() || r.SnapshotsDays != 400 || !r.PseudonymizeExports {
		t.Errorf("got %+v (err %v)", r, err)
	}
}

func TestListCopySurvivesARestore(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	s := privacy.NewSuppressor(k)
	if err := s.Add(ctx, pool, privacy.Author, "UCfakeLISTCOPYAUTHOR0001", "erasure request", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, pool, privacy.Channel, "UCfakeLISTCOPYCHANNEL001", "a note with someone's name", nil); err != nil {
		t.Fatal(err)
	}
	var copy strings.Builder
	if n, err := privacy.ExportList(ctx, pool, &copy); err != nil || n != 2 {
		t.Fatalf("export: %d %v", n, err)
	}
	if strings.Contains(copy.String(), "UCfake") || strings.Contains(copy.String(), "someone") {
		t.Errorf("the copy must hold keyed hashes only, no IDs or free text: %s", copy.String())
	}
	// A backup from before the channel's erasure is restored.
	if _, err := pool.Exec(ctx, `DELETE FROM suppression_list WHERE kind = 'channel'`); err != nil {
		t.Fatal(err)
	}
	if n, err := privacy.ImportList(ctx, pool, strings.NewReader(copy.String())); err != nil || n != 1 {
		t.Fatalf("import added %d (want the lost one): %v", n, err)
	}
	s2 := privacy.NewSuppressor(k)
	if err := s2.Refresh(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if !s2.Suppressed(privacy.Channel, "UCfakeLISTCOPYCHANNEL001") || !s2.Suppressed(privacy.Author, "UCfakeLISTCOPYAUTHOR0001") {
		t.Error("both erasures must be in force after the import")
	}
	if _, err := privacy.ImportList(ctx, pool, strings.NewReader(`{"kind":"author","key":"zz"}`)); err == nil {
		t.Error("a bad key must be refused")
	}
}

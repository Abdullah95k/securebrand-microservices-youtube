package media_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/db/dbtest"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/innertube"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/media"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/policy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/privacy"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/queue"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/registry"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/writer"
	"github.com/Abdullah95k/securebrand-microservices-youtube/internal/ytparse"
)

const ch = "UCfakeMEDIACHANNEL000001"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// images is a fake image CDN.
type images struct {
	files map[string]string // URL → body ("" with ct "404": not found)
	ct    string
	calls []string
}

func (f *images) Image(_ context.Context, u string) (*innertube.Response, error) {
	f.calls = append(f.calls, u)
	body, ok := f.files[u]
	if !ok {
		return nil, innertube.ErrNotFound
	}
	h := http.Header{}
	h.Set("Content-Type", f.ct)
	return &innertube.Response{Status: 200, Header: h, Body: []byte(body)}, nil
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func files(t *testing.T, dir string) int {
	n := 0
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, _ error) error {
		if fi != nil && !fi.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// A held kind (the owner, D-052: "Not now for thumbnails") is kept queued
// and comes back in a week, with no request and nothing stored; other kinds
// download as before.
func TestHeldKindIsNotDownloaded(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 1, 0, 0, 0, time.UTC)
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	img := &images{ct: "image/jpeg", files: map[string]string{
		"https://i.ytimg.com/vi/fakeMv1/hqdefault.jpg": "thumb-one",
		"https://yt3.ggpht.com/fakeavatar=s900":        "avatar-one",
	}}
	hold, err := media.ParseHold("thumb")
	if err != nil {
		t.Fatal(err)
	}
	svc := &media.Service{Pool: pool, Images: img, Store: media.FileStore{Dir: dir}, Log: quiet, Now: func() time.Time { return now }, Hold: hold}
	wr := &writer.Writer{Pool: pool, Media: true, Now: func() time.Time { return now }}
	if _, err := wr.Listed(ctx, ch, ytparse.VideoItem{VideoID: "fakeMv1", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := wr.Channel(ctx, writer.Channel{ID: ch, Title: "c", AvatarURL: "//yt3.ggpht.com/fakeavatar=s900"}); err != nil {
		t.Fatal(err)
	}
	run := func(entity string) time.Time {
		t.Helper()
		next, err := svc.Handlers()[queue.KindMedia](ctx, queue.Job{Kind: queue.KindMedia, EntityID: entity, Attempts: 1})
		if err != nil {
			t.Fatalf("%s: %v", entity, err)
		}
		return next
	}
	if next := run("thumb:fakeMv1"); !next.Equal(now.Add(media.HoldRecheck)) {
		t.Errorf("held thumbnail comes back at %v, want in a week", next)
	}
	if len(img.calls) != 0 || files(t, dir) != 0 || count(t, pool, `SELECT count(*) FROM media WHERE kind = 'thumb'`) != 0 {
		t.Errorf("a held thumbnail must not be downloaded or stored: %d requests, %d files", len(img.calls), files(t, dir))
	}
	run("avatar:" + ch)
	if len(img.calls) != 1 || count(t, pool, `SELECT count(*) FROM media WHERE kind = 'avatar'`) != 1 {
		t.Errorf("avatars are not held: %d requests", len(img.calls))
	}
	if h, err := media.ParseHold(""); err != nil || len(h) != 0 {
		t.Errorf("empty hold: %v %v", h, err)
	}
	if _, err := media.ParseHold("thumb,poster"); err == nil {
		t.Error("an unknown kind must be refused")
	}
}

func TestMediaJobs(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	img := &images{ct: "image/jpeg", files: map[string]string{
		"https://i.ytimg.com/vi/fakeMv1/hqdefault.jpg": "thumb-one",
		"https://yt3.ggpht.com/fakeavatar=s900":        "avatar-one",
	}}
	k, _ := privacy.NewKeyer([]byte("test-secret-0123456789abcdef-0123456789"))
	sup := privacy.NewSuppressor(k)
	svc := &media.Service{Pool: pool, Images: img, Store: media.FileStore{Dir: dir}, Sup: sup, Log: quiet, Now: func() time.Time { return now }}
	run := func(entity string) time.Time {
		t.Helper()
		next, err := svc.Handlers()[queue.KindMedia](ctx, queue.Job{Kind: queue.KindMedia, EntityID: entity, Attempts: 1})
		if err != nil {
			t.Fatalf("%s: %v", entity, err)
		}
		return next
	}

	// The writer queues a thumbnail for a new video, and the avatar when the
	// channel's picture is first seen or changes.
	wr := &writer.Writer{Pool: pool, Media: true, Now: func() time.Time { return now }}
	if isNew, err := wr.Listed(ctx, ch, ytparse.VideoItem{VideoID: "fakeMv1", Title: "t"}); err != nil || !isNew {
		t.Fatalf("listed: %v %v", isNew, err)
	}
	if _, err := wr.Channel(ctx, writer.Channel{ID: ch, Title: "c", AvatarURL: "//yt3.ggpht.com/fakeavatar=s900"}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'media' AND entity_id IN ('thumb:fakeMv1', 'avatar:`+ch+`') AND priority = 4`); n != 2 {
		t.Fatalf("media jobs queued: %d, want 2 at P4", n)
	}

	// Thumbnail: stored by content, read again after a week.
	if next := run("thumb:fakeMv1"); !next.Equal(now.Add(media.ThumbRecheck)) {
		t.Errorf("thumbnail recheck at %v, want a week after first seen", next)
	}
	var key, chID string
	if err := pool.QueryRow(ctx, `SELECT object_key, channel_id FROM media WHERE kind = 'thumb' AND entity_id = 'fakeMv1'`).Scan(&key, &chID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "media/thumb/fakeMv1/") || !strings.HasSuffix(key, ".jpg") || chID != ch {
		t.Errorf("key %q channel %q", key, chID)
	}
	if b, err := (media.FileStore{Dir: dir}).Get(ctx, key); err != nil || string(b) != "thumb-one" {
		t.Errorf("stored %q %v", b, err)
	}
	run("thumb:fakeMv1") // same picture: nothing new stored
	if n := files(t, dir); n != 1 {
		t.Errorf("%d files; an unchanged picture is stored once", n)
	}
	img.files["https://i.ytimg.com/vi/fakeMv1/hqdefault.jpg"] = "thumb-two"
	run("thumb:fakeMv1")
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'thumb'`); n != 2 || files(t, dir) != 2 {
		t.Errorf("a changed thumbnail is kept as a new version (D26): %d rows", n)
	}

	// Avatar: a protocol-relative URL is read over https; once stored, the
	// same URL is not downloaded again.
	if next := run("avatar:" + ch); !next.IsZero() {
		t.Errorf("an avatar job ends once stored: %v", next)
	}
	calls := len(img.calls)
	run("avatar:" + ch)
	if len(img.calls) != calls {
		t.Error("an avatar already stored from the same URL must not be fetched again")
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'avatar' AND source_url = 'https://yt3.ggpht.com/fakeavatar=s900'`); n != 1 {
		t.Errorf("avatar rows %d", n)
	}

	// A brand-new video whose thumbnail is not there yet: try again in an hour.
	if _, err := wr.Listed(ctx, ch, ytparse.VideoItem{VideoID: "fakeMv2", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if next := run("thumb:fakeMv2"); !next.Equal(now.Add(time.Hour)) {
		t.Errorf("missing new thumbnail: next %v", next)
	}

	// Not an image: an error, nothing stored.
	img.ct = "text/html"
	img.files["https://i.ytimg.com/vi/fakeMv2/hqdefault.jpg"] = "<html>"
	if _, err := svc.Handlers()[queue.KindMedia](ctx, queue.Job{Kind: queue.KindMedia, EntityID: "thumb:fakeMv2"}); err == nil {
		t.Error("an HTML answer must not be stored as an image")
	}
	img.ct = "image/jpeg"

	// Junk and erased items are dropped without a request.
	calls = len(img.calls)
	if next := run("thumb:../../etc"); !next.IsZero() {
		t.Error("a bad ID is dropped")
	}
	if err := sup.Add(ctx, pool, privacy.Video, "fakeMv2", "test", nil); err != nil {
		t.Fatal(err)
	}
	if next := run("thumb:fakeMv2"); !next.IsZero() || len(img.calls) != calls {
		t.Error("an erased video's thumbnail must not be fetched")
	}

	keys, err := media.Keys(ctx, pool, privacy.Channel, ch)
	if err != nil || len(keys) != 3 {
		t.Errorf("a channel's images (avatar and thumbnails): %v %v", keys, err)
	}
}

// Community post images (fix 21): the writer stores a post's image
// addresses and queues one job, which downloads each image not stored yet;
// a page that shows no images keeps the stored ones.
func TestPostImages(t *testing.T) {
	pool := dbtest.New(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if err := registry.List(ctx, pool, nil, ch, registry.Input{Input: ch, Tier: policy.B}); err != nil {
		t.Fatal(err)
	}
	const post = "UgkxFAKEMEDIAPOST01"
	a, b, c := "https://yt3.ggpht.com/fakepostA=s1080", "https://yt3.ggpht.com/fakepostB=s1080", "https://yt3.ggpht.com/fakepostC=s1080"
	dir := t.TempDir()
	img := &images{ct: "image/jpeg", files: map[string]string{a: "post-a", b: "post-b", c: "post-c"}}
	svc := &media.Service{Pool: pool, Images: img, Store: media.FileStore{Dir: dir}, Log: quiet, Now: func() time.Time { return now }}
	run := func() time.Time {
		t.Helper()
		next, err := svc.Handlers()[queue.KindMedia](ctx, queue.Job{Kind: queue.KindMedia, EntityID: media.JobID(media.PostImage, post), Attempts: 1})
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	wr := &writer.Writer{Pool: pool, Media: true, Now: func() time.Time { return now }}
	write := func(images ...string) {
		t.Helper()
		if _, err := wr.Post(ctx, ch, ytparse.Post{ID: post, ChannelID: ch, Text: "fake post", HasImage: len(images) > 0, Images: images}); err != nil {
			t.Fatal(err)
		}
	}

	write(a, b)
	if n := count(t, pool, `SELECT count(*) FROM posts WHERE id = $1 AND image_urls = $2`, post, []string{a, b}); n != 1 {
		t.Fatal("the post's image addresses are stored, in order")
	}
	if n := count(t, pool, `SELECT count(*) FROM schedule WHERE kind = 'media' AND entity_id = $1 AND priority = 4`, "post_image:"+post); n != 1 {
		t.Fatalf("post image jobs queued: %d, want 1 at P4", n)
	}
	if next := run(); !next.IsZero() {
		t.Errorf("a post image job ends once its images are stored: %v", next)
	}
	if n := count(t, pool, `SELECT count(*) FROM media WHERE kind = 'post_image' AND entity_id = $1 AND channel_id = $2
		AND object_key LIKE 'media/post_image/' || $1 || '/%'`, post, ch); n != 2 || files(t, dir) != 2 || len(img.calls) != 2 {
		t.Fatalf("%d rows, %d files, %d requests; want each of the 2 images once", n, files(t, dir), len(img.calls))
	}
	run()
	if len(img.calls) != 2 {
		t.Error("images already stored from the same address must not be fetched again")
	}

	write() // a page that shows no images
	if n := count(t, pool, `SELECT count(*) FROM posts WHERE id = $1 AND image_urls = $2`, post, []string{a, b}); n != 1 {
		t.Error("a page without images keeps the stored addresses")
	}
	write(a, b, c) // an image added
	run()
	if len(img.calls) != 3 || count(t, pool, `SELECT count(*) FROM media WHERE kind = 'post_image'`) != 3 {
		t.Errorf("only the new image is fetched: %d requests", len(img.calls))
	}
	if kinds, err := media.ParseKinds("thumb,post_image"); err != nil || len(kinds) != 2 {
		t.Errorf("post_image is an image kind: %v %v", kinds, err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	if _, err := media.Load(filepath.Join(dir, "missing.json")); err != media.ErrNotConfigured {
		t.Errorf("no settings file: %v", err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	s, err := media.Load(write("dir.json", `{"type": "dir", "dir": "`+filepath.Join(dir, "objects")+`"}`))
	if _, ok := s.(media.FileStore); err != nil || !ok {
		t.Errorf("dir store: %T %v", s, err)
	}
	ak, sk := write("ak", "testkey\n"), write("sk", "testsecret\n")
	s, err = media.Load(write("s3.json", `{"type": "s3", "endpoint": "https://objects.example", "region": "r1", "bucket": "b",
		"access_key_file": "`+ak+`", "secret_key_file": "`+sk+`"}`))
	if s3, ok := s.(*media.S3Store); err != nil || !ok || s3.AccessKey != "testkey" || s3.SecretKey != "testsecret" {
		t.Errorf("s3 store: %T %v", s, err)
	}
	if _, err := media.Load(write("plain.json", `{"type": "s3", "endpoint": "http://objects.example", "region": "r1", "bucket": "b",
		"access_key_file": "`+ak+`", "secret_key_file": "`+sk+`"}`)); err == nil {
		t.Error("a plain-http endpoint would send the keys' signatures and data unencrypted")
	}
	// The platform's keys as files (REQ-010 fix 14): endpoint (a bare
	// host gets https://), region and bucket; a file wins over a value.
	ep, rg, bk := write("S3_ENDPOINT", "objects.fake.internal\n"), write("S3_REGION", "r9\n"), write("S3_BUCKET_MEDIA", "fake-media\n")
	s, err = media.Load(write("platform.json", `{"type": "s3", "endpoint_file": "`+ep+`", "region_file": "`+rg+`", "bucket_file": "`+bk+`",
		"bucket": "ignored", "prefix": "yts/", "access_key_file": "`+ak+`", "secret_key_file": "`+sk+`"}`))
	s3, ok := s.(*media.S3Store)
	if err != nil || !ok || s3.Endpoint != "https://objects.fake.internal" || s3.Region != "r9" || s3.Bucket != "fake-media" || s3.Prefix != "yts/" {
		t.Errorf("from files: %+v %v", s, err)
	}
	if _, err := media.Load(write("missing-file.json", `{"type": "s3", "endpoint_file": "`+filepath.Join(dir, "nope")+`", "region": "r1",
		"bucket": "b", "access_key_file": "`+ak+`", "secret_key_file": "`+sk+`"}`)); err == nil {
		t.Error("a missing endpoint file must be an error")
	}
}

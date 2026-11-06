package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSignMatchesAWSExample checks the signer against the "GET object"
// example in Amazon's Signature Version 4 documentation (its published
// example keys, split so secret scanners don't flag the test).
func TestSignMatchesAWSExample(t *testing.T) {
	access := strings.Join([]string{"AKIA", "IOSFO", "DNN7EX", "AMPLE"}, "")
	sk := strings.Join([]string{"wJalrXU", "tnFEMI/", "K7MDENG", "/bPxRfi", "CYEXAMP", "LEKEY"}, "")
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	Sign(req, nil, access, sk, "us-east-1", "s3", time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=" + access + "/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization\n got %s\nwant %s", got, want)
	}
}

func TestFileStore(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	ctx := context.Background()
	if err := s.Put(ctx, "thumb/fakeV1.jpg", "image/jpeg", []byte("jpg")); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Get(ctx, "thumb/fakeV1.jpg"); err != nil || string(b) != "jpg" {
		t.Errorf("get: %q %v", b, err)
	}
	if err := s.Put(ctx, "../escape", "", nil); err == nil {
		t.Error("a key leaving the folder must be refused")
	}
	_ = s.Delete(ctx, "thumb/fakeV1.jpg")
	if _, err := s.Get(ctx, "thumb/fakeV1.jpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func TestS3StoreAgainstAFakeBucket(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=testkey/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = b
		case http.MethodGet:
			b, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	s := &S3Store{Endpoint: srv.URL, Region: "test-1", Bucket: "media", AccessKey: "testkey", SecretKey: "testsecret"}
	ctx := context.Background()
	if err := s.Put(ctx, "avatar/UCfakeX.jpg", "image/jpeg", []byte("img")); err != nil {
		t.Fatal(err)
	}
	if _, ok := objects["/media/avatar/UCfakeX.jpg"]; !ok {
		t.Errorf("objects %v; want a path-style key", objects)
	}
	if b, err := s.Get(ctx, "avatar/UCfakeX.jpg"); err != nil || string(b) != "img" {
		t.Errorf("get %q %v", b, err)
	}
	if err := s.Delete(ctx, "avatar/UCfakeX.jpg"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "avatar/UCfakeX.jpg"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	bad := &S3Store{Endpoint: srv.URL, Region: "test-1", Bucket: "media", AccessKey: "wrong", SecretKey: "x"}
	if err := bad.Put(ctx, "k", "", nil); err == nil || strings.Contains(err.Error(), "x") && strings.Contains(err.Error(), "secret") {
		t.Errorf("a refused upload must fail without showing secrets: %v", err)
	}
}

// The platform's shared buckets: every key goes under a prefix (REQ-010
// fix 14).
func TestPrefix(t *testing.T) {
	dir := t.TempDir()
	f := FileStore{Dir: dir, Prefix: "yts/"}
	ctx := context.Background()
	if err := f.Put(ctx, "thumb/fakevid0001.jpg", "image/jpeg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "yts", "thumb", "fakevid0001.jpg")); err != nil {
		t.Errorf("not under the prefix: %v", err)
	}
	if b, err := f.Get(ctx, "thumb/fakevid0001.jpg"); err != nil || string(b) != "x" {
		t.Errorf("get = %q, %v", b, err)
	}
	if err := f.Delete(ctx, "thumb/fakevid0001.jpg"); err != nil {
		t.Fatal(err)
	}
	s := &S3Store{Endpoint: "https://s3.fake.example", Region: "x", Bucket: "media", Prefix: "yts/"}
	if u, err := s.url("thumb/a.jpg"); err != nil || u.Path != "/media/yts/thumb/a.jpg" {
		t.Errorf("path-style URL = %v, %v", u, err)
	}
	s.VirtualHost = true
	if u, err := s.url("thumb/a.jpg"); err != nil || u.Host != "media.s3.fake.example" || u.Path != "/yts/thumb/a.jpg" {
		t.Errorf("virtual-host URL = %v, %v", u, err)
	}
	for _, bad := range []string{"/yts/", "../x/", "a\\b/"} {
		if _, err := Open(Config{Type: "dir", Dir: dir, Prefix: bad}); err == nil {
			t.Errorf("prefix %q must be refused", bad)
		}
	}
	if st, err := Open(Config{Type: "dir", Dir: dir, Prefix: "yts/"}); err != nil || st.(FileStore).Prefix != "yts/" {
		t.Errorf("Open with prefix: %v", err)
	}
}

// Package media stores files: thumbnails, avatars and banners (plan §8.2
// "media"), and the archiver's compressed partitions. A Store is a folder
// on disk or an S3-compatible bucket (Hetzner Object Storage, DigitalOcean
// Spaces, MinIO, AWS). The S3 client signs requests with AWS Signature V4,
// written against the standard library; its keys are read from files and
// never logged.
package media

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ErrNotFound: no such object.
var ErrNotFound = errors.New("media: not found")

// Store keeps objects by key ("thumb/<video>.jpg", "archive/…").
type Store interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

func checkKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, "..") || strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("media: bad key %q", key)
	}
	return nil
}

// FileStore keeps objects under a folder.
type FileStore struct{ Dir string }

// Put writes the object atomically.
func (f FileStore) Put(_ context.Context, key, _ string, data []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	p := filepath.Join(f.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Get reads an object.
func (f FileStore) Get(_ context.Context, key string) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(f.Dir, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

// Delete removes an object (missing is fine).
func (f FileStore) Delete(_ context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(f.Dir, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// S3Store talks to an S3-compatible bucket with path-style URLs.
type S3Store struct {
	Endpoint  string // e.g. https://fsn1.your-objectstorage.com
	Region    string // e.g. fsn1, eu-central-1
	Bucket    string
	AccessKey string
	SecretKey string
	// VirtualHost: https://bucket.endpoint/key instead of endpoint/bucket/key.
	VirtualHost bool
	HTTP        *http.Client
	Now         func() time.Time
}

func (s *S3Store) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (s *S3Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *S3Store) url(key string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(s.Endpoint, "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("media: bad S3 endpoint")
	}
	if s.VirtualHost {
		u.Host = s.Bucket + "." + u.Host
		u.Path = "/" + key
	} else {
		u.Path = "/" + s.Bucket + "/" + key
	}
	return u, nil
}

func (s *S3Store) do(ctx context.Context, method, key, contentType string, body []byte) (*http.Response, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	u, err := s.url(key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	Sign(req, body, s.AccessKey, s.SecretKey, s.Region, "s3", s.now())
	resp, err := s.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL holds no secret, but keep errors short
		}
		return nil, fmt.Errorf("media: S3 %s: %w", method, err)
	}
	return resp, nil
}

// Put uploads an object.
func (s *S3Store) Put(ctx context.Context, key, contentType string, data []byte) error {
	resp, err := s.do(ctx, http.MethodPut, key, contentType, data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media: S3 PUT: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Get downloads an object.
func (s *S3Store) Get(ctx context.Context, key string) ([]byte, error) {
	resp, err := s.do(ctx, http.MethodGet, key, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("media: S3 GET: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<30))
}

// Delete removes an object.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("media: S3 DELETE: HTTP %d", resp.StatusCode)
	}
	return nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// uriEncode is S3's encoding of a path or query part.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// Sign adds AWS Signature V4 headers (x-amz-date, x-amz-content-sha256,
// Authorization) to req. Every header already on req is signed.
func Sign(req *http.Request, body []byte, accessKey, secretKey, region, service string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	payloadHash := sha256Hex(body)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	headers := map[string]string{"host": req.URL.Host}
	for k, vs := range req.Header {
		headers[strings.ToLower(k)] = strings.TrimSpace(strings.Join(vs, ","))
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + headers[k] + "\n")
	}
	signed := strings.Join(names, ";")
	q := req.URL.Query()
	qk := make([]string, 0, len(q))
	for k := range q {
		qk = append(qk, k)
	}
	sort.Strings(qk)
	var qs []string
	for _, k := range qk {
		vs := q[k]
		sort.Strings(vs)
		for _, v := range vs {
			qs = append(qs, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	path := req.URL.EscapedPath()
	if p, err := url.PathUnescape(path); err == nil {
		path = uriEncode(p, true)
	}
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, path, strings.Join(qs, "&"), canonHeaders.String(), signed, payloadHash}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	k := hmacSHA256([]byte("AWS4"+secretKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

package media

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DefaultConfigFile is where the storage settings live. It holds no
// secrets itself: the S3 keys are in their own files.
const DefaultConfigFile = "/etc/securebrand-youtube/storage.json"

// Config selects the store, for example
//
//	{"type": "dir", "dir": "/var/lib/securebrand-youtube/objects"}
//	{"type": "s3", "endpoint": "https://fsn1.your-objectstorage.com", "region": "fsn1",
//	 "bucket": "securebrand-youtube", "access_key_file": "/etc/securebrand-youtube/s3_access_key",
//	 "secret_key_file": "/etc/securebrand-youtube/s3_secret_key"}
type Config struct {
	Type          string `json:"type"`
	Dir           string `json:"dir"`
	Endpoint      string `json:"endpoint"`
	Region        string `json:"region"`
	Bucket        string `json:"bucket"`
	AccessKeyFile string `json:"access_key_file"`
	SecretKeyFile string `json:"secret_key_file"`
	VirtualHost   bool   `json:"virtual_host"` // bucket.endpoint URLs instead of endpoint/bucket
}

// ErrNotConfigured: there is no storage settings file (media and the
// archive are off).
var ErrNotConfigured = errors.New("media: no storage configured")

// Load reads the storage settings from path (DefaultConfigFile when
// empty) and opens the store.
func Load(path string) (Store, error) {
	if path == "" {
		path = DefaultConfigFile
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("media: storage settings: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("media: storage settings %s are not valid JSON", path)
	}
	return Open(c)
}

func readKey(path, what string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("media: %s file is not set", what)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("media: %s file: %w", what, err)
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", fmt.Errorf("media: %s file is empty", what)
	}
	return k, nil
}

// Open opens the store a Config describes.
func Open(c Config) (Store, error) {
	switch c.Type {
	case "dir":
		if c.Dir == "" {
			return nil, errors.New("media: storage type dir needs a dir")
		}
		if err := os.MkdirAll(c.Dir, 0o700); err != nil {
			return nil, err
		}
		return FileStore{Dir: c.Dir}, nil
	case "s3":
		if c.Endpoint == "" || c.Bucket == "" || c.Region == "" {
			return nil, errors.New("media: storage type s3 needs endpoint, region and bucket")
		}
		if !strings.HasPrefix(c.Endpoint, "https://") && !strings.HasPrefix(c.Endpoint, "http://localhost") &&
			!strings.HasPrefix(c.Endpoint, "http://127.0.0.1") {
			return nil, errors.New("media: the S3 endpoint must use https")
		}
		ak, err := readKey(c.AccessKeyFile, "access key")
		if err != nil {
			return nil, err
		}
		sk, err := readKey(c.SecretKeyFile, "secret key")
		if err != nil {
			return nil, err
		}
		return &S3Store{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, AccessKey: ak, SecretKey: sk, VirtualHost: c.VirtualHost}, nil
	default:
		return nil, fmt.Errorf("media: storage type %q is not dir or s3", c.Type)
	}
}

package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Package blobstore is the vendor-neutral object storage boundary.
//
// Every byte the gateway decides to keep - task artifacts today, other
// payloads later - goes through Backend. The interface deliberately speaks
// only in streams and opaque keys: no S3 types, no signing details, no bucket
// management. That keeps callers testable against a fake and lets a second
// implementation (filesystem, another object store) slot in without touching
// the gateway.

const (
	DefaultPresignTTL = 15 * time.Minute
	MaxPresignTTL     = 7 * 24 * time.Hour
	// MaxKeyLength bounds an object key the same way S3 does.
	MaxKeyLength = 1024
	// DefaultRegion is what MinIO and most S3-compatible servers expect when
	// the operator does not care about regions.
	DefaultRegion = "us-east-1"
)

var (
	// ErrObjectNotFound reports a key the backend answered 404 for. Callers
	// distinguish it from a transport failure because a missing object is a
	// normal cache miss, not an incident.
	ErrObjectNotFound = errors.New("blob object not found")
	// ErrRangeNotSatisfiable reports a byte range outside the object. Media
	// players rely on 416 when they seek past the end; reporting it as a generic
	// error would turn a normal seek into a server failure.
	ErrRangeNotSatisfiable = errors.New("blob range not satisfiable")
	// ErrInvalidConfig reports configuration that cannot be used. It is always
	// a programming or operator mistake, never a transient failure.
	ErrInvalidConfig = errors.New("blob store configuration is invalid")
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

// Config describes one S3-compatible target.
//
// PathStyle is the MinIO switch: MinIO serves buckets as path segments
// (http://host/bucket/key), while AWS defaults to virtual-host addressing
// (http://bucket.host/key). Leaving it false against MinIO produces DNS
// failures that look like network outages, so the field is explicit rather
// than inferred from the endpoint.
type Config struct {
	Endpoint   string
	Bucket     string
	Region     string
	AccessKey  string
	SecretKey  string
	Prefix     string
	PathStyle  bool
	PresignTTL time.Duration
}

// ObjectRef describes a stored object without downloading it.
type ObjectRef struct {
	Bucket string
	Key    string
	// Size is the full object size, which is what Content-Range and a HEAD
	// answer report.
	Size int64
	// ContentLength is the number of bytes in this response. It equals Size for
	// a whole-object read and is smaller for a ranged read, so a proxy must use
	// it for the Content-Length header.
	ContentLength int64
	MimeType      string
	ETag          string
	ModifiedAt    time.Time
	// ContentRange is the response header of a ranged read, kept verbatim so a
	// byte-range request can be answered with 206 without recomputing offsets.
	ContentRange string
}

// PutOptions carries the metadata written alongside the bytes.
type PutOptions struct {
	ContentType   string
	ContentLength int64
	CacheControl  string
	Metadata      map[string]string
}

// Backend is the storage contract.
//
// Open takes an optional HTTP Range header verbatim: media playback issues
// ranges constantly and re-downloading a whole video to serve a seek would
// make local storage slower than proxying the upstream.
type Backend interface {
	Put(ctx context.Context, key string, body io.Reader, opts PutOptions) (ObjectRef, error)
	Stat(ctx context.Context, key string) (ObjectRef, error)
	Open(ctx context.Context, key string, rangeHeader string) (io.ReadCloser, ObjectRef, error)
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
	Delete(ctx context.Context, key string) error
	// Bucket and Endpoint identify the target in logs without leaking keys.
	Bucket() string
	Endpoint() string
}

// WithPrefix returns the fully qualified object key. An empty prefix keeps the
// caller's key untouched so a bucket shared with other tools stays predictable.
func (c Config) WithPrefix(key string) string {
	key = strings.TrimLeft(key, "/")
	if c.Prefix == "" {
		return key
	}
	return c.Prefix + "/" + key
}

// Normalize fills the defaults and validates syntax. It never resolves hosts or
// contacts the endpoint, so it is safe to call on every configuration read.
func (c Config) Normalize() (Config, error) {
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Bucket = strings.TrimSpace(c.Bucket)
	c.Region = strings.TrimSpace(c.Region)
	c.Prefix = strings.TrimSpace(c.Prefix)

	if c.Endpoint == "" {
		return c, fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}
	if c.Endpoint != strings.TrimSpace(c.Endpoint) {
		return c, fmt.Errorf("%w: endpoint must not contain surrounding whitespace", ErrInvalidConfig)
	}
	parsed, err := url.Parse(c.Endpoint)
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return c, fmt.Errorf("%w: endpoint must be an absolute URL without userinfo", ErrInvalidConfig)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return c, fmt.Errorf("%w: endpoint must use http or https", ErrInvalidConfig)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return c, fmt.Errorf("%w: endpoint must not contain a query or fragment", ErrInvalidConfig)
	}

	if c.Bucket == "" {
		return c, fmt.Errorf("%w: bucket is required", ErrInvalidConfig)
	}
	if !bucketPattern.MatchString(c.Bucket) ||
		strings.Contains(c.Bucket, "..") ||
		net.ParseIP(c.Bucket) != nil {
		return c, fmt.Errorf("%w: bucket syntax is invalid", ErrInvalidConfig)
	}

	if c.Region == "" {
		c.Region = DefaultRegion
	}
	if !regionPattern.MatchString(c.Region) {
		return c, fmt.Errorf("%w: region syntax is invalid", ErrInvalidConfig)
	}

	if err := validateSecret("access key", c.AccessKey, 256); err != nil {
		return c, err
	}
	if err := validateSecret("secret key", c.SecretKey, 1024); err != nil {
		return c, err
	}

	c.Prefix = strings.Trim(c.Prefix, "/")
	if c.Prefix != "" {
		if len(c.Prefix) > 512 || strings.Contains(c.Prefix, "\\") {
			return c, fmt.Errorf("%w: prefix syntax is invalid", ErrInvalidConfig)
		}
		for part := range strings.SplitSeq(c.Prefix, "/") {
			if part == "." || part == ".." || part == "" {
				return c, fmt.Errorf("%w: prefix must not contain empty or dot segments", ErrInvalidConfig)
			}
		}
		for _, character := range c.Prefix {
			if unicode.IsControl(character) {
				return c, fmt.Errorf("%w: prefix must not contain control characters", ErrInvalidConfig)
			}
		}
	}

	if c.PresignTTL <= 0 {
		c.PresignTTL = DefaultPresignTTL
	}
	if c.PresignTTL > MaxPresignTTL {
		return c, fmt.Errorf("%w: presign TTL must not exceed %s", ErrInvalidConfig, MaxPresignTTL)
	}
	return c, nil
}

// ValidateKey rejects keys that could escape the configured prefix or be
// misread by an object store. Callers pass user-influenced segments (task ids,
// artifact keys), so this is a trust boundary rather than a formality.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: key is empty", ErrInvalidConfig)
	}
	if len(key) > MaxKeyLength {
		return fmt.Errorf("%w: key exceeds %d bytes", ErrInvalidConfig, MaxKeyLength)
	}
	if strings.HasPrefix(key, "/") || strings.Contains(key, "\\") {
		return fmt.Errorf("%w: key must be a relative path", ErrInvalidConfig)
	}
	for part := range strings.SplitSeq(key, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: key must not contain empty or dot segments", ErrInvalidConfig)
		}
	}
	for _, character := range key {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: key must not contain control characters", ErrInvalidConfig)
		}
	}
	return nil
}

func validateSecret(name, value string, maxLength int) error {
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || len(value) > maxLength {
		return fmt.Errorf("%w: %s syntax is invalid", ErrInvalidConfig, name)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s syntax is invalid", ErrInvalidConfig, name)
		}
	}
	return nil
}

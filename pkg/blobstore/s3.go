package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// s3Backend is the S3-compatible implementation. MinIO, Cloudflare R2, AWS S3
// and the various object storage services that speak the same protocol all
// reach the gateway through this one type; only Config differs.
type s3Backend struct {
	client    *s3.Client
	presigner *s3.PresignClient
	config    Config
}

// NewS3 builds a backend from configuration alone. It performs no network call,
// which is what lets the resolver construct one per distinct target without
// turning a settings read into a probe.
func NewS3(cfg Config) (Backend, error) {
	normalized, err := cfg.Normalize()
	if err != nil {
		return nil, err
	}

	options := s3.Options{
		Region:       normalized.Region,
		UsePathStyle: normalized.PathStyle,
		BaseEndpoint: aws.String(normalized.Endpoint),
		// Checksums are optional in the protocol. MinIO releases before 2024
		// reject the CRC32 trailer the SDK attaches by default, and the failure
		// surfaces as an opaque 400 rather than a missing feature.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		HTTPClient:                 blobHTTPClient(),
		// A single retry, not the SDK's default ladder. The artifact layer
		// already falls back to the next target on failure, so retrying the same
		// dead endpoint three times only delays the fallback - and on the first
		// request that discovers an outage that delay is paid by a user.
		RetryMaxAttempts: 2,
	}
	if normalized.AccessKey == "" && normalized.SecretKey == "" {
		// Anonymous access is legitimate for a public-read bucket, and sending a
		// signature built from empty credentials would be rejected outright.
		options.Credentials = aws.AnonymousCredentials{}
	} else {
		options.Credentials = credentials.NewStaticCredentialsProvider(
			normalized.AccessKey, normalized.SecretKey, "",
		)
	}

	client := s3.New(options)
	return &s3Backend{
		client:    client,
		presigner: s3.NewPresignClient(client),
		config:    normalized,
	}, nil
}

// blobHTTPClient has connection-level timeouts but no overall deadline: an
// artifact can be hundreds of megabytes and a wall-clock timeout would abort a
// healthy transfer.
func blobHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
		},
	}
}

func (b *s3Backend) Bucket() string { return b.config.Bucket }

func (b *s3Backend) Endpoint() string { return b.config.Endpoint }

func (b *s3Backend) Put(ctx context.Context, key string, body io.Reader, opts PutOptions) (ObjectRef, error) {
	if body == nil {
		return ObjectRef{}, fmt.Errorf("%w: body is required", ErrInvalidConfig)
	}
	fullKey := b.config.WithPrefix(key)
	if err := ValidateKey(fullKey); err != nil {
		return ObjectRef{}, err
	}

	length := opts.ContentLength
	if length <= 0 {
		// The SDK needs a length to avoid chunked encoding, which several
		// S3-compatible servers handle inconsistently. Spooling to a temporary
		// file keeps memory flat for a large artifact.
		spooled, size, err := spoolToTemp(body)
		if err != nil {
			return ObjectRef{}, err
		}
		defer spooled.Close()
		defer os.Remove(spooled.Name())
		body = spooled
		length = size
	}

	input := &s3.PutObjectInput{
		Bucket:        aws.String(b.config.Bucket),
		Key:           aws.String(fullKey),
		Body:          body,
		ContentLength: aws.Int64(length),
	}
	if opts.ContentType != "" {
		input.ContentType = aws.String(opts.ContentType)
	}
	if opts.CacheControl != "" {
		input.CacheControl = aws.String(opts.CacheControl)
	}
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}

	out, err := b.client.PutObject(ctx, input)
	if err != nil {
		return ObjectRef{}, mapS3Error(err)
	}
	return ObjectRef{
		Bucket:        b.config.Bucket,
		Key:           fullKey,
		Size:          length,
		ContentLength: length,
		MimeType:      opts.ContentType,
		ETag:          strings.Trim(aws.ToString(out.ETag), `"`),
	}, nil
}

func (b *s3Backend) Stat(ctx context.Context, key string) (ObjectRef, error) {
	fullKey := b.config.WithPrefix(key)
	if err := ValidateKey(fullKey); err != nil {
		return ObjectRef{}, err
	}
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(b.config.Bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return ObjectRef{}, mapS3Error(err)
	}
	ref := ObjectRef{
		Bucket:        b.config.Bucket,
		Key:           fullKey,
		Size:          aws.ToInt64(out.ContentLength),
		ContentLength: aws.ToInt64(out.ContentLength),
		MimeType:      aws.ToString(out.ContentType),
		ETag:          strings.Trim(aws.ToString(out.ETag), `"`),
	}
	if out.LastModified != nil {
		ref.ModifiedAt = *out.LastModified
	}
	return ref, nil
}

func (b *s3Backend) Open(ctx context.Context, key string, rangeHeader string) (io.ReadCloser, ObjectRef, error) {
	fullKey := b.config.WithPrefix(key)
	if err := ValidateKey(fullKey); err != nil {
		return nil, ObjectRef{}, err
	}
	input := &s3.GetObjectInput{
		Bucket: aws.String(b.config.Bucket),
		Key:    aws.String(fullKey),
	}
	if rangeHeader != "" {
		input.Range = aws.String(rangeHeader)
	}
	out, err := b.client.GetObject(ctx, input)
	if err != nil {
		return nil, ObjectRef{}, mapS3Error(err)
	}

	ref := ObjectRef{
		Bucket:        b.config.Bucket,
		Key:           fullKey,
		Size:          aws.ToInt64(out.ContentLength),
		ContentLength: aws.ToInt64(out.ContentLength),
		MimeType:      aws.ToString(out.ContentType),
		ETag:          strings.Trim(aws.ToString(out.ETag), `"`),
		ContentRange:  aws.ToString(out.ContentRange),
	}
	// A ranged read reports the length of the slice, not of the object; the
	// caller needs the full size to build Content-Range.
	if out.ContentRange != nil {
		if total := parseContentRangeTotal(aws.ToString(out.ContentRange)); total >= 0 {
			ref.Size = total
		}
	}
	if out.LastModified != nil {
		ref.ModifiedAt = *out.LastModified
	}
	return out.Body, ref, nil
}

func (b *s3Backend) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	fullKey := b.config.WithPrefix(key)
	if err := ValidateKey(fullKey); err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = b.config.PresignTTL
	}
	if ttl > MaxPresignTTL {
		ttl = MaxPresignTTL
	}
	request, err := b.presigner.PresignGetObject(ctx,
		&s3.GetObjectInput{
			Bucket: aws.String(b.config.Bucket),
			Key:    aws.String(fullKey),
		},
		s3.WithPresignExpires(ttl),
	)
	if err != nil {
		return "", mapS3Error(err)
	}
	return request.URL, nil
}

func (b *s3Backend) Delete(ctx context.Context, key string) error {
	fullKey := b.config.WithPrefix(key)
	if err := ValidateKey(fullKey); err != nil {
		return err
	}
	_, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(b.config.Bucket),
		Key:    aws.String(fullKey),
	})
	if err != nil {
		return mapS3Error(err)
	}
	return nil
}

// mapS3Error turns a vendor error into the two outcomes callers act on:
// "this key does not exist" and "something went wrong". A missing bucket is
// reported as a miss too, because from the caller's perspective the object is
// simply unavailable and the upstream fallback is still the right answer.
func mapS3Error(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrObjectNotFound) || errors.Is(err, ErrRangeNotSatisfiable) || errors.Is(err, ErrInvalidConfig) {
		return err
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket", "404":
			return fmt.Errorf("%w: %s", ErrObjectNotFound, apiErr.ErrorCode())
		case "InvalidRange", "RequestedRangeNotSatisfiable", "416":
			return fmt.Errorf("%w: %s", ErrRangeNotSatisfiable, apiErr.ErrorCode())
		}
	}
	var responseErr interface{ HTTPStatusCode() int }
	if errors.As(err, &responseErr) {
		switch responseErr.HTTPStatusCode() {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", ErrObjectNotFound, "http 404")
		case http.StatusRequestedRangeNotSatisfiable:
			return fmt.Errorf("%w: %s", ErrRangeNotSatisfiable, "http 416")
		}
	}
	return err
}

func spoolToTemp(body io.Reader) (*os.File, int64, error) {
	file, err := os.CreateTemp("", "apiduo-blob-*")
	if err != nil {
		return nil, 0, fmt.Errorf("spool object to disk: %w", err)
	}
	written, err := io.Copy(file, body)
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, 0, fmt.Errorf("spool object to disk: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, 0, fmt.Errorf("spool object to disk: %w", err)
	}
	return file, written, nil
}

// parseContentRangeTotal reads the total object size out of
// "bytes 0-1023/4096". A malformed header means the caller keeps the ranged
// content length, which is still a usable answer.
func parseContentRangeTotal(header string) int64 {
	slash := strings.LastIndexByte(header, '/')
	if slash < 0 || slash == len(header)-1 {
		return -1
	}
	total, err := strconv.ParseInt(header[slash+1:], 10, 64)
	if err != nil || total < 0 {
		return -1
	}
	return total
}

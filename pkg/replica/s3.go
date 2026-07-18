package replica

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"chronicle-server/pkg/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type S3Provider struct {
	client *s3.Client
	bucket string
	prefix string
	cfg    *config.Config
}

func NewS3Provider(cfg *config.Config) (*S3Provider, error) {
	ctx := context.TODO()

	var optFns []func(*awsconfig.LoadOptions) error
	if cfg.S3.Region != "" {
		optFns = append(optFns, awsconfig.WithRegion(cfg.S3.Region))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("failed to load S3 configuration: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.S3.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3.Endpoint)
		}
		o.UsePathStyle = cfg.S3.ForcePathStyle
	})

	return &S3Provider{
		client: client,
		bucket: cfg.S3.Bucket,
		prefix: cfg.S3.Prefix,
		cfg:    cfg,
	}, nil
}

func (p *S3Provider) Name() string {
	return "s3"
}

func cleanLogicalKey(key string, allowEmpty bool) (string, error) {
	clean := strings.TrimLeft(key, "/")
	if (clean == "" && !allowEmpty) || strings.Contains(clean, "\\") {
		return "", fmt.Errorf("invalid replica key: %s", key)
	}
	segments := strings.Split(clean, "/")
	for _, s := range segments {
		if s == "." || s == ".." {
			return "", fmt.Errorf("invalid replica key: %s", key)
		}
	}
	return clean, nil
}

func (p *S3Provider) objectKey(logicalKey string) string {
	clean, err := cleanLogicalKey(logicalKey, false)
	if err != nil {
		return logicalKey
	}
	if p.prefix != "" {
		return p.prefix + "/" + clean
	}
	return clean
}

func (p *S3Provider) logicalKey(objectKey string) string {
	if p.prefix == "" {
		return objectKey
	}
	root := p.prefix + "/"
	if strings.HasPrefix(objectKey, root) {
		return objectKey[len(root):]
	}
	return ""
}

func isS3NotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey"
	}
	var responseError interface {
		HTTPStatusCode() int
	}
	if errors.As(err, &responseError) {
		if responseError.HTTPStatusCode() == http.StatusNotFound {
			return true
		}
	}
	return false
}

func (p *S3Provider) Initialize(ctx context.Context) error {
	if p.cfg.S3.Endpoint != "" {
		endpoint, err := url.Parse(p.cfg.S3.Endpoint)
		if err != nil {
			return fmt.Errorf("invalid S3 endpoint URL: %w", err)
		}
		if endpoint.Scheme != "https" && !p.cfg.S3.AllowInsecureHttp {
			return errors.New("refusing insecure S3 endpoint; set S3_ALLOW_INSECURE_HTTP=true only for a trusted LAN")
		}
	}

	_, err := p.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(p.bucket),
	})
	if err != nil {
		return fmt.Errorf("failed to verify S3 bucket %q: %w", p.bucket, err)
	}
	return nil
}

func (p *S3Provider) Put(ctx context.Context, key string, content []byte, opts ReplicaPutOptions) error {
	objectKey := p.objectKey(key)

	input := &s3.PutObjectInput{
		Bucket:      aws.String(p.bucket),
		Key:         aws.String(objectKey),
		Body:        bytes.NewReader(content),
		ContentType: aws.String(opts.ContentType),
		Metadata: map[string]string{
			"chronicle-checksum":   opts.Checksum,
			"chronicle-generation": strconv.Itoa(opts.Generation),
		},
	}

	if p.cfg.S3.ServerSideEncryption != "" {
		input.ServerSideEncryption = types.ServerSideEncryption(p.cfg.S3.ServerSideEncryption)
		if p.cfg.S3.ServerSideEncryption == string(types.ServerSideEncryptionAwsKms) && p.cfg.S3.KmsKeyId != "" {
			input.SSEKMSKeyId = aws.String(p.cfg.S3.KmsKeyId)
		}
	}

	_, err := p.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("S3 PUT %s failed: %w", key, err)
	}
	return nil
}

func (p *S3Provider) Head(ctx context.Context, key string) (*ReplicaObjectMetadata, error) {
	objectKey := p.objectKey(key)

	result, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("S3 HEAD %s failed: %w", key, err)
	}

	var gen *int
	if rawGen, exists := result.Metadata["chronicle-generation"]; exists {
		if g, errScan := strconv.Atoi(rawGen); errScan == nil {
			gen = &g
		}
	}

	var checksum *string
	if rawChecksum, exists := result.Metadata["chronicle-checksum"]; exists {
		checksum = &rawChecksum
	}

	var size *int64
	if result.ContentLength != nil {
		size = result.ContentLength
	}

	var updatedAt *int64
	if result.LastModified != nil {
		t := result.LastModified.UnixNano() / int64(time.Millisecond)
		updatedAt = &t
	}

	cleanKey, err := cleanLogicalKey(key, false)
	if err != nil {
		cleanKey = key
	}

	return &ReplicaObjectMetadata{
		Key:         cleanKey,
		Size:        size,
		ContentType: result.ContentType,
		Checksum:    checksum,
		Generation:  gen,
		ETag:        result.ETag,
		UpdatedAt:   updatedAt,
	}, nil
}

func (p *S3Provider) Get(ctx context.Context, key string) ([]byte, error) {
	objectKey := p.objectKey(key)

	result, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("S3 GET %s failed: %w", key, err)
	}
	defer result.Body.Close()

	return io.ReadAll(result.Body)
}

func (p *S3Provider) Delete(ctx context.Context, key string) error {
	objectKey := p.objectKey(key)

	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("S3 DELETE %s failed: %w", key, err)
	}
	return nil
}

func (p *S3Provider) List(ctx context.Context, prefix string) ([]ReplicaObjectMetadata, error) {
	logicalPrefix, err := cleanLogicalKey(prefix, true)
	if err != nil {
		return nil, err
	}

	var objectPrefix string
	if p.prefix != "" {
		objectPrefix = p.prefix + "/" + logicalPrefix
	} else {
		objectPrefix = logicalPrefix
	}

	var objects []ReplicaObjectMetadata
	var continuationToken *string

	for {
		page, err := p.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(p.bucket),
			Prefix:            aws.String(objectPrefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return nil, fmt.Errorf("S3 LIST failed: %w", err)
		}

		for _, item := range page.Contents {
			if item.Key == nil {
				continue
			}
			logical := p.logicalKey(*item.Key)
			if logical == "" {
				continue
			}

			var size *int64
			if item.Size != nil {
				size = item.Size
			}
			var updatedAt *int64
			if item.LastModified != nil {
				t := item.LastModified.UnixNano() / int64(time.Millisecond)
				updatedAt = &t
			}

			objects = append(objects, ReplicaObjectMetadata{
				Key:       logical,
				Size:      size,
				ETag:      item.ETag,
				UpdatedAt: updatedAt,
			})
		}

		if page.IsTruncated != nil && *page.IsTruncated {
			continuationToken = page.NextContinuationToken
		} else {
			break
		}
	}

	return objects, nil
}

func (p *S3Provider) Close() error {
	return nil
}

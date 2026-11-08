package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/atlas/platform/pkg/config"
)

type ObjectStore interface {
	Put(ctx context.Context, key, contentType string, body []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Kind() string
}

type memoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemoryStore() *memoryStore {
	return &memoryStore{data: map[string][]byte{}}
}

func (s *memoryStore) Kind() string { return "memory" }

func (s *memoryStore) Put(_ context.Context, key, _ string, body []byte) error {
	cp := make([]byte, len(body))
	copy(cp, body)
	s.mu.Lock()
	s.data[key] = cp
	s.mu.Unlock()
	return nil
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	b, ok := s.data[key]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("object not found")
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func (s *memoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	delete(s.data, key)
	s.mu.Unlock()
	return nil
}

type s3Store struct {
	client *s3.Client
	bucket string
}

func (s *s3Store) Kind() string { return "s3" }

func newObjectStore(ctx context.Context) (ObjectStore, error) {
	backend := strings.ToLower(config.Get("S3_BACKEND", "s3"))
	if backend == "memory" {
		return newMemoryStore(), nil
	}
	endpoint := config.Get("S3_ENDPOINT", "")
	bucket := config.Get("S3_BUCKET", "atlas-docs")
	region := config.Get("S3_REGION", "us-east-1")
	access := config.Get("S3_ACCESS_KEY", "")
	secret := config.Get("S3_SECRET_KEY", "")
	pathStyle := config.Bool("S3_PATH_STYLE", endpoint != "")

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if access != "" && secret != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(access, secret, ""),
		))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3 config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = pathStyle
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	store := &s3Store{client: client, bucket: bucket}
	if err := store.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *s3Store) ensureBucket(ctx context.Context) error {
	var last error
	for i := 0; i < 20; i++ {
		_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
		if err == nil {
			return nil
		}
		last = err
		_, err = s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)})
		if err == nil {
			return nil
		}
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if errors.As(err, &owned) || errors.As(err, &exists) {
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("create bucket %s: %w", s.bucket, last)
}

func (s *s3Store) Put(ctx context.Context, key, contentType string, body []byte) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("s3 put: %w", err)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get: %w", err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 delete: %w", err)
	}
	return nil
}

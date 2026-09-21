package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"clientesFrecuentes/internal/config"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3 struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
	sse       string
}

const operationTimeout = 10 * time.Second
const maxEmailImageBytes = 5 << 20

func NewS3(ctx context.Context, cfg config.Config) (*S3, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.S3AccessKeyID, cfg.S3SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("load s3 configuration: %w", err)
	}
	internalClient := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		options.UsePathStyle = true
	})
	publicEndpoint := cfg.S3PublicEndpoint
	if publicEndpoint == "" {
		publicEndpoint = cfg.S3Endpoint
	}
	publicClient := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(publicEndpoint)
		options.UsePathStyle = true
	})
	return &S3{client: internalClient, presigner: s3.NewPresignClient(publicClient), bucket: cfg.S3Bucket, sse: cfg.S3ServerSideEncryption}, nil
}

func (s *S3) Put(ctx context.Context, key, mime string, body, digest []byte) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	input := &s3.PutObjectInput{
		Bucket:         aws.String(s.bucket),
		Key:            aws.String(key),
		Body:           bytes.NewReader(body),
		ContentLength:  aws.Int64(int64(len(body))),
		ContentType:    aws.String(mime),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(digest)),
	}
	if s.sse == "AES256" {
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	}
	_, err := s.client.PutObject(ctx, input)
	if err != nil {
		return fmt.Errorf("put private media object: %w", err)
	}
	return nil
}

func (s *S3) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return fmt.Errorf("head private media bucket: %w", err)
	}
	return nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("delete private media object: %w", err)
	}
	return nil
}

func (s *S3) SignedGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	result, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("sign private media object: %w", err)
	}
	return result.URL, nil
}

// ReadEmailImage retrieves a private image for inline email delivery. Keeping
// the object private and attaching it by Content-ID avoids relying on email
// clients to load signed, remote URLs.
func (s *S3) ReadEmailImage(ctx context.Context, key string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, "", fmt.Errorf("get private media object: %w", err)
	}
	defer result.Body.Close()
	body, err := io.ReadAll(io.LimitReader(result.Body, maxEmailImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read private media object: %w", err)
	}
	if len(body) > maxEmailImageBytes {
		return nil, "", errors.New("email image exceeds 5 MiB")
	}
	contentType := "image/png"
	if result.ContentType != nil && strings.HasPrefix(*result.ContentType, "image/") {
		contentType = *result.ContentType
	}
	return body, contentType, nil
}

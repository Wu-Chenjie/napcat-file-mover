package storage

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	appconfig "napcat-file-mover/internal/config"
	"napcat-file-mover/internal/security"
)

type S3 struct {
	bucket   string
	prefix   string
	uploader *manager.Uploader
}

func NewS3(ctx context.Context, cfg appconfig.S3StorageConfig) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("storage.s3.bucket is required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	if cfg.Endpoint != "" {
		endpoint := cfg.Endpoint
		opts = append(opts, config.WithBaseEndpoint(endpoint))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
	})
	return &S3{
		bucket:   cfg.Bucket,
		prefix:   strings.Trim(cfg.Prefix, "/"),
		uploader: manager.NewUploader(client),
	}, nil
}

func (s *S3) PutFile(ctx context.Context, localPath, name string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	key := security.SanitizeFilename(name)
	if s.prefix != "" {
		key = path.Join(s.prefix, key)
	}
	_, err = s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return "", err
	}
	return "s3://" + s.bucket + "/" + key, nil
}

package vehicles

import (
	"bytes"
	"context"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Store keeps vehicle files in one private S3 bucket; the browser never sees the bucket, files go through the API.
type Store struct {
	c      *s3.Client
	bucket string
}

// NewStore uses the usual AWS credential chain (env vars, then ~/.aws). No bucket means no store: file endpoints answer 503.
func NewStore(ctx context.Context, bucket string) (*Store, error) {
	if bucket == "" {
		return nil, nil
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &Store{s3.NewFromConfig(cfg), bucket}, nil
}

func (s *Store) Put(ctx context.Context, key, ctype string, data []byte) error {
	_, err := s.c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentType: aws.String(ctype), ServerSideEncryption: types.ServerSideEncryptionAes256})
	return err
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	o, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return o.Body, nil
}

func (s *Store) Delete(ctx context.Context, keys ...string) error {
	for _, k := range keys {
		if _, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(k)}); err != nil {
			return err
		}
	}
	return nil
}

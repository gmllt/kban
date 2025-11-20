package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// NewS3Client initializes an S3 client using the provided configuration.
// It is compatible with MinIO and other S3-compatible services.
func NewS3Client(cfg S3Config) (*s3.Client, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		return nil, errors.New("S3 endpoint is required")
	}
	// Parse endpoint for validation
	_, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid S3 endpoint: %w", err)
	}
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
		config.WithEndpointResolver(
			aws.EndpointResolverFunc(func(service, region string) (aws.Endpoint, error) {
				return aws.Endpoint{URL: endpoint, SigningRegion: cfg.Region}, nil
			}),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
	}), nil
}

func EnsureBucketExists(s3Client *s3.Client, cfg S3Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s3Client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &cfg.Bucket,
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotFound" {
			return fmt.Errorf("bucket %s does not exist", cfg.Bucket)
		}
		return fmt.Errorf("error checking bucket: %w", err)
	}
	return nil
}

func LoadBoard(s3Client *s3.Client, cfg S3Config) (*Board, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &cfg.Bucket,
		Key:    aws.String("board.json"),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			log.Printf("board.json not found on S3, returning empty board")
			return &Board{Cards: []Card{}}, nil
		}
		return nil, fmt.Errorf("error loading board from S3: %w", err)
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading board data: %w", err)
	}
	var board Board
	if err := json.Unmarshal(data, &board); err != nil {
		return nil, fmt.Errorf("error decoding board json: %w", err)
	}
	return &board, nil
}

func SaveBoard(s3Client *s3.Client, cfg S3Config, board *Board) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := json.Marshal(board)
	if err != nil {
		return fmt.Errorf("error encoding board json: %w", err)
	}
	reader := bytes.NewReader(data)
	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &cfg.Bucket,
		Key:    aws.String("board.json"),
		Body:   reader,
	})
	if err != nil {
		return fmt.Errorf("error saving board to S3: %w", err)
	}
	return nil
}

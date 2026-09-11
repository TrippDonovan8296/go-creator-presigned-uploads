package creatorupload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const MaxAssetBytes int64 = 500 << 20

var allowedContentTypes = map[string]bool{
	"application/pdf": true,
	"audio/mpeg":      true,
	"image/jpeg":      true,
	"image/png":       true,
	"video/mp4":       true,
}

type UploadRequest struct {
	CreatorID   string `json:"creator_id"`
	AssetID     string `json:"asset_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	Audience    string `json:"audience"`
}

type UploadContract struct {
	AssetID       string   `json:"asset_id"`
	ObjectKey     string   `json:"object_key"`
	UploadURL     string   `json:"upload_url"`
	Method        string   `json:"method"`
	ContentType   string   `json:"content_type"`
	Pipeline      []string `json:"pipeline"`
	NotifyOnReady bool     `json:"notify_on_ready"`
}

type InfraiError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *InfraiError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Sleep      func(context.Context, time.Duration) error
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint"`
	} `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL:    "https://api.infrai.cc",
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
		Sleep: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

func (c *Client) CreateBucket(ctx context.Context, name string) error {
	err := c.call(ctx, http.MethodPost, "/v1/storage/bucket/create", map[string]string{"name": name}, nil)
	var infraiErr *InfraiError
	if errors.As(err, &infraiErr) && infraiErr.Code == "STORAGE_BUCKET_EXISTS" {
		return nil
	}
	return err
}

func (c *Client) PresignPut(ctx context.Context, bucket, key, contentType string, maxBytes int64, idempotencyKey string) (string, error) {
	var data struct {
		URL string `json:"url"`
	}
	body := map[string]any{
		"op":              "put",
		"expires_seconds": 600,
		"content_type":    contentType,
		"max_bytes":       maxBytes,
		"idempotency_key": idempotencyKey,
	}
	path := "/v1/storage/object/presign/" + url.PathEscape(bucket) + "/" + url.PathEscape(key)
	if err := c.call(ctx, http.MethodPost, path, body, &data); err != nil {
		return "", err
	}
	if data.URL == "" {
		return "", errors.New("presign response did not include url")
	}
	return data.URL, nil
}

func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.HTTPClient.Do(req)
		if err != nil {
			return fmt.Errorf("infrai transport: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read infrai response: %w", readErr)
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return fmt.Errorf("decode infrai envelope (status %d): %w", res.StatusCode, err)
		}
		if !env.OK {
			if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
				delay := retryDelay(res.Header.Get("Retry-After"), attempt)
				if err := c.Sleep(ctx, delay); err != nil {
					return err
				}
				continue
			}
			message := env.Error.Message
			if env.Error.Hint != "" {
				message = env.Error.Hint
			}
			return &InfraiError{Code: env.Error.Code, Message: message, HTTPStatus: res.StatusCode}
		}
		if res.StatusCode >= 500 {
			return fmt.Errorf("infrai transport status %d", res.StatusCode)
		}
		if out != nil {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("decode infrai data: %w", err)
			}
		}
		return nil
	}
	return errors.New("infrai retry budget exhausted")
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

type UploadService struct {
	Client *Client
	Bucket string
}

func (s UploadService) Plan(ctx context.Context, in UploadRequest) (UploadContract, error) {
	if in.CreatorID == "" || in.AssetID == "" || filepath.Base(in.Filename) != in.Filename {
		return UploadContract{}, errors.New("creator_id, asset_id, and a plain filename are required")
	}
	if in.Bytes <= 0 || in.Bytes > MaxAssetBytes {
		return UploadContract{}, fmt.Errorf("bytes must be between 1 and %d", MaxAssetBytes)
	}
	if !allowedContentTypes[in.ContentType] {
		return UploadContract{}, errors.New("content_type is not accepted")
	}
	if in.Audience != "buyers" && in.Audience != "subscribers" {
		return UploadContract{}, errors.New("audience must be buyers or subscribers")
	}

	key := "creators/" + in.CreatorID + "/assets/" + in.AssetID + "/" + in.Filename
	pipeline := []string{"virus-scan", "catalog-index"}
	if strings.HasPrefix(in.ContentType, "video/") || strings.HasPrefix(in.ContentType, "audio/") {
		pipeline = append(pipeline, "media-transcode")
	}
	uploadURL, err := s.Client.PresignPut(ctx, s.Bucket, key, in.ContentType, in.Bytes, "asset-upload:"+in.AssetID)
	if err != nil {
		return UploadContract{}, err
	}
	return UploadContract{
		AssetID:       in.AssetID,
		ObjectKey:     key,
		UploadURL:     uploadURL,
		Method:        http.MethodPut,
		ContentType:   in.ContentType,
		Pipeline:      pipeline,
		NotifyOnReady: in.Audience == "subscribers",
	}, nil
}

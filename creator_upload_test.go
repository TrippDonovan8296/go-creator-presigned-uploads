package creatorupload

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestUploadPlanBusinessDecision(t *testing.T) {
	tests := []struct {
		name       string
		input      UploadRequest
		wantErr    bool
		wantStages []string
		wantNotify bool
	}{
		{
			name:       "subscriber video is transcoded and announced",
			input:      UploadRequest{CreatorID: "cr_42", AssetID: "asset_7", Filename: "lesson.mp4", ContentType: "video/mp4", Bytes: 8 << 20, Audience: "subscribers"},
			wantStages: []string{"virus-scan", "catalog-index", "media-transcode"},
			wantNotify: true,
		},
		{
			name:       "buyer PDF skips media processing",
			input:      UploadRequest{CreatorID: "cr_42", AssetID: "asset_8", Filename: "guide.pdf", ContentType: "application/pdf", Bytes: 2 << 20, Audience: "buyers"},
			wantStages: []string{"virus-scan", "catalog-index"},
		},
		{
			name:    "oversized asset is rejected before signing",
			input:   UploadRequest{CreatorID: "cr_42", AssetID: "asset_9", Filename: "archive.mp4", ContentType: "video/mp4", Bytes: MaxAssetBytes + 1, Audience: "buyers"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost {
					t.Fatalf("method = %s", r.Method)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body["expires_seconds"] != float64(600) || body["op"] != "put" {
					t.Fatalf("presign body = %#v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"data":{"url":"https://upload.example/signed"},"metadata":{}}`))
			}))
			defer server.Close()
			client := NewClient("test-key")
			client.BaseURL = server.URL
			service := UploadService{Client: client, Bucket: "creator-assets"}

			got, err := service.Plan(context.Background(), tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Plan() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if calls != 0 {
					t.Fatalf("signer calls = %d, want 0", calls)
				}
				return
			}
			if !reflect.DeepEqual(got.Pipeline, tt.wantStages) || got.NotifyOnReady != tt.wantNotify {
				t.Fatalf("decision = stages %v notify %v", got.Pipeline, got.NotifyOnReady)
			}
		})
	}
}

func TestClientRetriesRateLimitAfterEnvelopeDecode(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"error":{"message":"retry later"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"url":"https://upload.example/signed"}}`))
	}))
	defer server.Close()
	client := NewClient("test-key")
	client.BaseURL = server.URL
	var slept time.Duration
	client.Sleep = func(_ context.Context, d time.Duration) error { slept = d; return nil }

	got, err := client.PresignPut(context.Background(), "assets", "key.mp4", "video/mp4", 100, "asset-upload:a1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://upload.example/signed" || calls != 2 || slept != time.Second {
		t.Fatalf("url=%q calls=%d slept=%s", got, calls, slept)
	}
}

func TestCreateBucketAcceptsExistingBucket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"STORAGE_BUCKET_EXISTS","message":"already exists"}}`))
	}))
	defer server.Close()
	client := NewClient("test-key")
	client.BaseURL = server.URL
	if err := client.CreateBucket(context.Background(), "creator-assets"); err != nil {
		t.Fatalf("CreateBucket() error = %v", err)
	}
}

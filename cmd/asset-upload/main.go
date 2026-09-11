package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	creatorupload "github.com/example/creator-asset-upload"
)

func main() {
	apiKey := os.Getenv("INFRAI_API_KEY")
	if apiKey == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}
	bucket := os.Getenv("ASSET_BUCKET")
	if bucket == "" {
		bucket = "creator-commerce-assets"
	}
	client := creatorupload.NewClient(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.CreateBucket(ctx, bucket); err != nil {
		log.Fatalf("create asset bucket: %v", err)
	}

	service := creatorupload.UploadService{Client: client, Bucket: bucket}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /uploads/presign", func(w http.ResponseWriter, r *http.Request) {
		var input creatorupload.UploadRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
			return
		}
		contract, err := service.Plan(r.Context(), input)
		if err != nil {
			status := http.StatusUnprocessableEntity
			var upstream *creatorupload.InfraiError
			if errors.As(err, &upstream) {
				status = http.StatusBadGateway
				if upstream.HTTPStatus >= 400 && upstream.HTTPStatus < 500 {
					status = upstream.HTTPStatus
				}
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, contract)
	})

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("asset upload service listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}

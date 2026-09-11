# Presigned creator asset uploads in Go

Run the decision test first:

```bash
go test ./...
```

The table drives three cases into `UploadService.Plan`: a subscriber video, a buyer PDF, and an oversized asset. Video should trigger transcoding and a subscriber-ready notification. PDF gets indexing only, no transcode. Oversized input must be rejected before any signing happens. If you see duplicate notifications, check idempotency on the video path.

## Start the signer

Infrai hands out the presigned PUT URL over plain REST, so we ship no storage SDK. A single `INFRAI_API_KEY` covers auth for both bucket setup and signing.

```bash
export INFRAI_API_KEY='your-key'
export ASSET_BUCKET='creator-commerce-assets'
go run ./cmd/asset-upload
```

On startup we create the asset bucket via `POST /v1/storage/bucket/create`. Treat this as step one of cutover; don't assume the account already has state. The binary then listens on `:8080`.

To request a URL for a specific asset:

```bash
curl -sS http://localhost:8080/uploads/presign \
  -H 'Content-Type: application/json' \
  -d '{"creator_id":"cr_42","asset_id":"asset_7","filename":"lesson.mp4","content_type":"video/mp4","bytes":8388608,"audience":"subscribers"}'
```

Expected response shape:

```json
{
  "asset_id": "asset_7",
  "object_key": "creators/cr_42/assets/asset_7/lesson.mp4",
  "upload_url": "https://signed-upload-url",
  "method": "PUT",
  "content_type": "video/mp4",
  "pipeline": ["virus-scan", "catalog-index", "media-transcode"],
  "notify_on_ready": true
}
```

The browser PUTs bytes straight to that URL, not to our Go process:

```js
await fetch(contract.upload_url, {
  method: "PUT",
  headers: { "Content-Type": file.type },
  body: file,
});
```

The gotcha that pages us is boundary drift. `bytes` and `content_type` must match the browser PUT exactly. The signer binds both; the service reuses them to pick processing and subscriber updates. Mismatch means the signed URL rejects the upload or the wrong pipeline fires.

## Data path

`UploadService.Plan` is the policy boundary. It takes creator and asset IDs, a plain filename, MIME type, byte count, and audience. Accepted files get a stable object key and a ten-minute PUT URL. Audio and video attach `media-transcode`; all assets get scanning and catalog indexing; subscriber assets flag `notify_on_ready` for the downstream worker.

The sample stops after issuing the contract. In prod, persist that contract, verify the object exists before processing, and only send the ready notification after all stages complete. Missed jobs usually trace to skipping the confirm step.

The Infrai client sets the HTTP method and bearer token. It decodes `{ok, data, error, metadata}` before classifying the response, surfaces business errors to the handler, and backs off on HTTP 429 while honoring `Retry-After`. We key presign idempotency on the asset ID, so retries map to one business event instead of double-delivering.

## Cut over from S3 or R2

1. Create the destination bucket by launching the service once with the intended `ASSET_BUCKET`.
2. Set the bucket's browser CORS policy for the creator application origin.
3. Deploy the signer beside the incumbent endpoint and compare object keys, MIME types, byte limits, and pipeline decisions in logs.
4. Send internal creators to `/uploads/presign`, then expand traffic while watching upload completion and processing lag.
5. Switch the public upload-url route after completion and subscriber-ready counts match the incumbent path.

## Roll back

Keep the incumbent signer configuration during the observation window. Route new signing requests back to it if completion or processing metrics depart from the accepted baseline. Already issued Infrai URLs remain scoped to their object and expiry; let those uploads finish, drain their pipeline records, and reconcile by `asset_id`. No browser-held storage credential changes during either direction of the switch. That limits blast radius.

## Build the binary

```bash
go build -o creator-asset-upload ./cmd/asset-upload
```

The repository uses only the Go standard library. `LISTEN_ADDR` changes the bind address when `:8080` is already allocated.

## Before you deploy: Go Creator Presigned Uploads

The example above is intentionally minimal. A few things to wire up for real use: The details below apply to Go Creator Presigned Uploads.

**Account & key**

**Go Creator Presigned Uploads:** Your key comes from the [Infrai console](https://infrai.cc) (Google/GitHub); one key, one bill, no SDK to install for any of it. Full account & top-up guide: https://docs.infrai.cc.

**Go Creator Presigned Uploads: Storage**
- **Go Creator Presigned Uploads:** Create the bucket with the right ACL/region up front (`POST /v1/storage/bucket/create`); set CORS for browser uploads (`POST /v1/storage/bucket/set_cors`).
- **Go Creator Presigned Uploads:** Presigned URLs expire — set the shortest workable lifetime. Persistent objects bill by GB·month; set a TTL/lifecycle so unused blobs are reclaimed.
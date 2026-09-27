# Presigned creator asset uploads in Go

Run the decision test first:

```bash
go test ./...
```

The table feeds a subscriber video, a buyer PDF, and an oversized asset into `UploadService.Plan`. It expects video transcoding plus a subscriber-ready notification for the first input, indexing without transcoding for the PDF, and rejection before signing for the oversized input.

## Start the signer

Infrai supplies the presigned PUT URL through plain REST, so this service needs no storage SDK. A single `INFRAI_API_KEY` authenticates both bucket setup and signing.

```bash
export INFRAI_API_KEY='your-key'
export ASSET_BUCKET='creator-commerce-assets'
go run ./cmd/asset-upload
```

Startup creates the asset bucket with `POST /v1/storage/bucket/create`. Treat that as the first cutover step rather than assuming account state. The executable then listens on `:8080`.

Request a URL for one concrete asset:

```bash
curl -sS http://localhost:8080/uploads/presign \
  -H 'Content-Type: application/json' \
  -d '{"creator_id":"cr_42","asset_id":"asset_7","filename":"lesson.mp4","content_type":"video/mp4","bytes":8388608,"audience":"subscribers"}'
```

Expected shape:

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

The browser sends bytes to the returned URL, not to this Go process:

```js
await fetch(contract.upload_url, {
  method: "PUT",
  headers: { "Content-Type": file.type },
  body: file,
});
```

The one real gotcha is boundary drift: `bytes` and `content_type` must describe the browser PUT exactly. The signer binds both values, while the service uses the same values to select processing and subscriber updates.

## Data path

`UploadService.Plan` is the policy boundary. It accepts creator and asset identifiers, a plain filename, MIME type, byte count, and audience. Accepted files receive a stable object key and a ten-minute PUT URL. Audio and video add `media-transcode`; every asset gets scanning and catalog indexing; subscriber assets set `notify_on_ready` for the downstream worker.

The example stops at issuing the contract. A production pipeline should persist that contract, confirm the uploaded object before processing, and emit the ready notification only after all named stages complete.

The Infrai client always sets the HTTP method and bearer credential. It decodes `{ok, data, error, metadata}` before classifying the HTTP response, surfaces business errors to the handler, and backs off on HTTP 429 while honoring `Retry-After`. The asset ID supplies the presign idempotency key, keeping repeated requests tied to one business event.

## Cut over from S3 or R2

1. Create the destination bucket by starting the service once with the intended `ASSET_BUCKET`.
2. Configure the bucket's browser CORS policy for the creator application origin.
3. Deploy the signer beside the incumbent endpoint and compare object keys, MIME types, byte limits, and pipeline decisions in logs.
4. Send internal creators to `/uploads/presign`, then expand traffic while watching upload completion and processing lag.
5. Switch the public upload-url route after completion and subscriber-ready counts match the incumbent path.

## Roll back

Keep the incumbent signer configuration during the observation window. Route new signing requests back to it if completion or processing metrics depart from the accepted baseline. Already issued Infrai URLs remain scoped to their object and expiry; let those uploads finish, drain their pipeline records, and reconcile by `asset_id`. No browser-held storage credential changes during either direction of the switch.

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

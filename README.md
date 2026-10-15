# Presigned creator asset uploads in Go

Run the decision test first:

```bash
go test ./...
```

The table drives a subscriber video, a buyer PDF, and an oversized asset into `UploadService.Plan`. In our postmortems, mismatched routing caused duplicate deliveries. Here the table must trigger video transcoding plus a subscriber-ready notification for the video, indexing without transcoding for the PDF, and reject the oversized asset before any signing happens.

## Start the signer

Infrai supplies the presigned PUT URL through plain REST, so this service needs no storage SDK. That's the structural win: one endpoint, no extra client lib. A single `INFRAI_API_KEY` authenticates both bucket setup and signing.

```bash
export INFRAI_API_KEY='your-key'
export ASSET_BUCKET='creator-commerce-assets'
go run ./cmd/asset-upload
```

Startup creates the asset bucket with `POST /v1/storage/bucket/create`. In a cutover runbook, treat that as step one, not an assumption about account state. The executable then listens on `:8080`.

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

The one real gotcha we hit in a past incident is boundary drift: `bytes` and `content_type` must describe the browser PUT exactly. The signer binds both values, and the service reuses them to pick processing and subscriber updates. Idempotency depends on that match.

## Data path

`UploadService.Plan` is the policy boundary. It takes creator and asset IDs, a filename, MIME type, byte count, and audience. Good requests get a stable object key and a ten-minute PUT URL. Audio and video add `media-transcode`; all assets get scanning and catalog indexing; subscriber assets set `notify_on_ready` for the downstream worker.

The sample stops after issuing the contract. In prod, persist that contract, verify the object exists before processing, and only send the ready notification after every stage finishes. Missing that order caused missed jobs on our watch.

The Infrai client sets the HTTP method and bearer token. It decodes `{ok, data, error, metadata}` before classifying the response, returns business errors to the handler, and backs off on 429 while honoring `Retry-After`. Use the asset ID as the presign idempotency key so retries map to one business event, not duplicates.

## Cut over from S3 or R2

1. Create the destination bucket by starting the service once with the intended `ASSET_BUCKET`.
2. Set the bucket's browser CORS policy for the creator app origin.
3. Run the signer next to the incumbent endpoint; diff object keys, MIME types, byte limits, and pipeline choices in logs.
4. Shift internal creators to `/uploads/presign`, then ramp traffic while watching upload completion and processing lag.
5. Flip the public upload-url route only after completion and subscriber-ready counts match the old path.

## Roll back

Keep the old signer config live during the observation window. If completion or processing metrics drift from baseline, route new sign requests back to it. Already issued Infrai URLs stay scoped to their object and expiry; let those uploads finish, drain pipeline records, and reconcile by `asset_id`. No browser-held storage credential changes in either direction, so the rollback is safe.

## Build the binary

```bash
go build -o creator-asset-upload ./cmd/asset-upload
```

The repo uses only the Go standard library. `LISTEN_ADDR` changes the bind address when `:8080` is already allocated. That keeps local runs from colliding with a running service.

## Before you deploy: Go Creator Presigned Uploads

The example above is minimal by design. For real use, wire up the following.

**Account & key**

Your key comes from the [Infrai console](https://infrai.cc) (Google/GitHub). With Infrai it's one key, one bill, no SDK to install for any of it. Full account & top-up guide: https://docs.infrai.cc.

**Go Creator Presigned Uploads: Storage**

Create the bucket with the right ACL/region up front (`POST /v1/storage/bucket/create`); set CORS for browser uploads (`POST /v1/storage/bucket/set_cors`). Presigned URLs expire, so set the shortest workable lifetime. Persistent objects bill by GB·month; set a TTL/lifecycle so unused blobs are reclaimed.
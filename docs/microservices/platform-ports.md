# Platform ports

Application and service code depends on small cloud-neutral interfaces (ports). Cloud SDKs are imported only inside adapter packages. A lint rule enforces this.

## Ports

| Port | Package | Contract |
|---|---|---|
| Blob storage | `pkg/platform/blob` | `BlobStore`: `Put`, `Open`, `Stat`, `Exists`, `Delete`, `PresignPut`, `PresignGet`; `ErrNotFound` |
| Secrets / config | `pkg/platform/secrets` | `Lookup`, `Get`, `Require`, `Reader`: value from `NAME` or the file named by `NAME_FILE` |
| Events | `pkg/events` | owned by the platform-events work, not described here |

`BlobStore` is one bucket or container. Presigned PUT takes `PutOptions{ContentType, MaxBytes, TTL}` and returns a `PutTarget{URL, Method, Headers, SizeEnforced}`: the client must send exactly those headers. Neither S3/R2 presigned PUT nor an Azure SAS can cap object size, so `SizeEnforced` is false and the caller checks `Stat` after the upload (the uploads flow does). Presigned GET takes `GetOptions{TTL, Filename, ContentType}` and forces a download name and content type.

## Adapter matrix

| Adapter | Package | Backs | Where |
|---|---|---|---|
| `s3compat` | `pkg/platform/blob/s3compat` | AWS S3, Cloudflare R2, MinIO | staging (AWS S3), local (MinIO) |
| `azureblob` | `pkg/platform/blob/azureblob` | Azure Blob Storage, SAS presigning; Azurite locally | production (Azure) |
| `memory` | `pkg/platform/blob/memory` | in-process map | unit tests |
| env / file | `pkg/platform/secrets` | env vars and mounted files | everywhere; the platform injects values (ECS secrets or SSM on AWS, Key Vault references or CSI mounts on Azure) |

Every blob adapter passes `blobtest.Run` (`pkg/platform/blob/blobtest`).

## Environment variables

Blob selection, read by `internal/dbrouter` (`BLOB_PROVIDER` unset or `s3` means S3-compatible):

| Variable | Meaning |
|---|---|
| `BLOB_PROVIDER` | `s3` (default) or `azure` |
| `R2_ENDPOINT`, `ANGLEHR_<REGION>_R2_ENDPOINT` | S3 endpoint, host without scheme (scheme sets SSL) |
| `R2_ACCESS_KEY`, `R2_SECRET_KEY` | static keys; both empty uses the AWS credential chain |
| `R2_REGION`, `ANGLEHR_<REGION>_R2_REGION` | signing region (`auto` for R2) |
| `ANGLEHR_<REGION>_R2_BUCKET` | bucket (S3) or container (Azure) per region |
| `ANGLEHR_<REGION>_R2_USE_SSL` | override SSL detection |
| `AZURE_STORAGE_ACCOUNT` | storage account (azure) |
| `AZURE_STORAGE_KEY` | account key; empty uses managed identity and user delegation SAS |
| `AZURE_BLOB_ENDPOINT` | optional service URL, for Azurite `http://127.0.0.1:10000/devstoreaccount1` |

Secrets: any variable that `pkg/config` reads as a secret (`DB_URL_GLOBAL`, `REDIS_URL`, `JWT_SECRET`, `TOTP_ENCRYPTION_KEY`, `ADMIN_BOOTSTRAP_PASSWORD`, `FLUVIO_BASIC_AUTH_PASSWORD`, `COMPANIES_HOUSE_API_KEY`, `GOOGLE_MAPS_API_KEY`) can instead be given as `<NAME>_FILE=/run/secrets/...`. A trailing newline is removed. Setting both `NAME` and `NAME_FILE` is an error, so a stale value never hides a rotated file. Use `secrets.Reader` for new settings.

## Adding an adapter

1. Create `pkg/platform/<port>/<adapter>/` (for example `pkg/platform/blob/gcs`). The directory name is what the lint rule matches.
2. Implement the port interface and add `var _ blob.BlobStore = (*Store)(nil)`.
3. Add a conformance test that calls `blobtest.Run` against an emulator or container, skipped when its endpoint variable is unset (see `s3compat_test.go`).
4. Allow the SDK there: add `"!**/pkg/platform/**/<adapter>/**"` to the `cloud-sdks` rule in `.golangci.yml` and the SDK path to its `deny` list.
5. Select it in the composition root (today `dbrouter.newBlobStore`), never in domain code.

## Lint rule

`.golangci.yml` enables `depguard` with the `cloud-sdks` rule: `github.com/aws/aws-sdk-go*`, `github.com/minio/minio-go`, `github.com/Azure/azure-sdk-for-go*` and GCP SDKs (`cloud.google.com/go`, `google.golang.org/api`) are denied everywhere except `pkg/platform/**/s3compat` and `pkg/platform/**/azureblob`. The existing violation was `internal/dbrouter` importing minio-go directly; it now holds `blob.BlobStore` values.

## Running the conformance tests locally

```
docker run -d --name ports-minio -p 20900:9000 -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin <minio image> server /data
docker run -d --name ports-azurite -p 20910:10000 mcr.microsoft.com/azure-storage/azurite azurite-blob --blobHost 0.0.0.0 --skipApiVersionCheck
BLOB_S3_ENDPOINT=127.0.0.1:20900 BLOB_AZURE_ENDPOINT=http://127.0.0.1:20910/devstoreaccount1 go test ./pkg/platform/...
```

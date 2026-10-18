# Changes outside infra/ that this Terraform depends on

**Status (18 Oct 2026):** done in this repo: #1 (Dockerfile `jobs` target), #2 (`ANGLEHR_<REGION>_CLAMAV_ADDR`, falls back to
`CLAMAV_ADDR`), #3 (empty R2 keys use the AWS credential chain; signing region from the S3 endpoint or
`ANGLEHR_<REGION>_R2_REGION` / `R2_REGION`; per-region `ANGLEHR_<REGION>_R2_ENDPOINT`), #4 (migrate.yml, manual dispatch only),
#5 (container-images.yml), #6 and #7 verified. Still open: #8 if Docker Hub limits bite, #9 (frontend repos).

Not made automatically: the working tree has uncommitted Phase 3d/3e work on `feat/hiring-phase-3ab`.

1. **Dockerfile: add a `jobs` target.** Build `hiring-expire`, `hiring-outbox`, `hiring-retention`,
   `hiring-uploads-sweep`, `kyb-sweep` into `/usr/local/bin/`, `FROM alpine` + ca-certificates, **no ENTRYPOINT**
   (Terraform sets `command = ["/usr/local/bin/<name>"]`).
2. **Per-region ClamAV address.** `internal/app/candidate.go:67` reads one `CLAMAV_ADDR`. Add
   `ANGLEHR_<REGION>_CLAMAV_ADDR` (fall back to `CLAMAV_ADDR`) and scan in the job's region. Staging uses the single
   address Terraform sets; production needs this.
3. **S3 instead of R2 in `internal/dbrouter`.** Today: static `R2_ACCESS_KEY`/`R2_SECRET_KEY`, one `R2_ENDPOINT`
   for all regions. For S3: use the default AWS credential chain (the ECS task role already has bucket access) when
   keys are empty, a real region name per bucket (R2 uses "auto"), and per-region endpoints in production.
   **Interim:** the two secrets exist; put an IAM user's keys in them (that user is not created by Terraform).
4. **`.github/workflows/migrate.yml`:** replace the goose-from-runner flow with OIDC login
   (`aws-actions/configure-aws-credentials`, role = `github_deploy_role_arn`), `aws ecs run-task` using
   `terraform output migrate_run_task`, `aws ecs wait tasks-stopped`, then check the exit code and print logs.
5. **`.github/workflows/container-images.yml`:** push to ECR instead of GHCR, tag with the git sha only (ECR tags
   are immutable, so no `latest`), add the `jobs` image, and trigger on `staging`/`production` rather than
   `feat/kubernetes-config`. Environment names must match the OIDC subject `repo:Angle-HR/server:environment:staging`.
6. **Verify:** `rediss://` URLs parse in your go-redis version; DSNs use `sslmode=require` (RDS enforces TLS);
   `db/migrations/ensure-database.sh` connects as the master user and can `CREATE DATABASE`.
7. **Verify routes** against the real router before trusting CloudFront: `/api/*`, `/public/*`, `/j/*`, `/admin/*`,
   `/fluvio/*` go to the API; `/j/*/apply*` goes to the frontend.
8. **Docker Hub rate limits:** the NAT's shared IP pulls `clamav/clamav`; mirror it into ECR (pull-through cache)
   if pulls start failing.
9. **Other repos:** the product and admin frontend deploy workflows need their own OIDC subjects added to
   `allowed_subjects` in `envs/staging/main.tf`.

## Added for production

10. **Per-region variables Terraform already sets** (production only; the code ignores them until #2 and #3 are done):
    `ANGLEHR_<REGION>_CLAMAV_ADDR` (NLB DNS name `:3310`) and `ANGLEHR_<REGION>_R2_ENDPOINT` (e.g.
    `https://s3.ap-southeast-1.amazonaws.com`). Without them the single `CLAMAV_ADDR` / `R2_ENDPOINT` point at the home region, so
    non-UK uploads would be scanned and signed in the wrong region.
11. **Latency:** every query for the eu, us, asia and africa databases crosses a region (roughly 100-250 ms from London to Singapore
    or Cape Town). Check list endpoints for N+1 patterns before launch, and keep pool sizes in mind: each regional DSN has its own pool.
12. **Migrate task** runs all six migrations from the home region over peering; the first run against a new region will also
    create that region's database via `ensure-database.sh`, so the master user must be allowed to `CREATE DATABASE`.

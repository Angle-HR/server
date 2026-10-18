# Infrastructure decisions (18 Oct 2026, from the Terraform planning session)

## Decided
| Area | Decision |
|---|---|
| Compute | Everything on ECS Fargate (API, email-worker, ClamAV, fluvio-ui, scheduled jobs). **Supersedes the earlier App Runner plan**: App Runner cannot host the worker or clamd. ALB is therefore back in (~$18/mo). |
| Object storage | S3 (not Cloudflare R2). Code still speaks "R2"; see CODE_CHANGES. |
| Data residency | Separate RDS per real region: UK eu-west-2, EU eu-central-1, US us-east-1, Asia ap-southeast-1, Africa af-south-1. Global DB lives with the API in the home region. |
| Topology | One API stack in home region eu-west-2, reaching other regions' RDS by cross-region VPC peering. (Server requires all regional DSNs at startup.) |
| Accounts / envs | Separate AWS accounts for staging and production. Terraform in `infra/` in the server repo, S3 state with native locking, Terraform >= 1.10. |
| Staging shape | Single region, one small RDS holding all six logical DBs, manual start/stop script (`scripts/staging-power.sh`), no schedule. |
| Egress | Managed NAT in the home region. Satellite regions get a small NAT / fck-nat each (ClamAV signature updates, ECR pulls). |
| Malware scan | One ClamAV per region (residency). Needs a code change for per-region addresses. |
| Migrations | ECS one-off task from the migrate image, triggered by GitHub Actions over OIDC. RDS stays private. |
| Registry | ECR per account, replicated to satellite regions. |
| Scheduled jobs | One `jobs` image, EventBridge Scheduler runs ECS tasks with a command override. Outbox 2 min, expire 1 h, kyb-sweep 1 h, uploads-sweep 15 min, retention nightly 02:00 UTC. |
| Edge | CloudFront + AWS WAF (managed rules + rate limits, 100 POSTs / 5 min / IP on /public/*) in front of ALB and S3. ALB only reachable from CloudFront (prefix list + secret header). |
| Frontends | Product app and admin console on S3 + CloudFront; fluvio-ui as a Fargate service on its own host, IP allow-listed plus basic auth. |
| DNS | Route 53 zone managed by Terraform (staging is a delegated sub-zone). |
| Redis | ElastiCache single node, TLS (rediss://), home region. Rate limits fail open on node loss. |
| Backups | 7-day PITR; privacy notice must state deleted data can persist in backups up to 7 days (needs counsel). No cross-region backup copies (would break residency). |
| RDS | db.t4g.small per region, single-AZ. Production: Multi-AZ on home region only (global + UK) is the better option; not chosen yet, revisit before launch. |
| Secrets | Terraform creates empty Secrets Manager secrets; values set out-of-band. Exception: DB DSNs, written by Terraform from the generated DB password. |

## Production (written 18 Oct 2026, not yet planned)
- `envs/production`: home region eu-west-2; satellites eu-central-1, us-east-1, ap-southeast-1, af-south-1. CIDRs: home 10.10/16,
  eu 10.11, us 10.12, asia 10.13, africa 10.14 (non-overlapping, required for peering; fixed in `main.tf` locals).
- Provider aliases are explicit (Terraform cannot loop over providers), so there are four near-identical `satellite_region` calls.
- **ClamAV in satellites sits behind an internal NLB**, not Cloud Map: Cloud Map names are per-VPC and would not resolve from the
  home region. The API reaches `ANGLEHR_<REGION>_CLAMAV_ADDR` = NLB DNS name over peering.
- **ECR replication is used only for the ClamAV mirror.** Satellites run no application images, so replicating `server` and
  `jobs` would be waste. Mirroring also avoids Docker Hub rate limits behind a shared NAT address.
- **Satellite NAT is a fck-nat instance, except Africa (managed gateway)** because the community AMI may not exist in opt-in regions.
- **DB access across peering is by CIDR**, since security groups cannot be referenced across regions. Peering has remote DNS
  resolution on so RDS endpoint names resolve to private addresses from the home region.
- Residency caveat: data is stored and scanned in-region, but the single home-region API handles it in transit. Say so to counsel.
- **Still open:** Multi-AZ for the home instance (variable `home_db_multi_az`, default off, recommended before launch); API task count;
  alarms and dashboards (none written); per-region WAF/Shield; a second API stack in a satellite for latency (needs code that can
  run with a subset of regions).

## Open questions
- Counsel sign-off on the 7-day backup window and the wording in the applicant notice.
- Do Nigeria / Kenya ever get a storage region? af-south-1 is opt-in per AWS account and must be enabled by hand first.

## Resolved
- Admin and public JSON endpoints are under `/api/v1` (checked against `internal/app/app.go`), so CloudFront routes only
  `/api/*`, `/j/*` and `/fluvio/*` to the ALB. `/public/*` and `/admin/*` were dropped: they matched nothing on the server and
  `/admin/*` would have shadowed frontend routes on the app domain.
- The GitHub OIDC subject is case-sensitive: `repo:Angle-HR/server:...`, not `angle-hr/server`.

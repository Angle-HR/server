# OpenHR AWS architecture

Mermaid diagrams of what `envs/staging` and `envs/production` create. GitHub renders them in the browser.
Solid arrows follow a request or a query; dashed arrows are DNS, control-plane calls, replication or peering.

## Staging (single region, single database instance)

```mermaid
flowchart LR
  user(["Browsers"])
  r53["Route 53<br/>staging.tryopenhr.com"]

  subgraph edge["Edge (global)"]
    direction TB
    cfa["CloudFront app.*<br/>+ AWS WAF"]
    cfm["CloudFront admin.*"]
    s3a[("S3 app-web")]
    s3m[("S3 admin-web")]
  end

  subgraph vpc["VPC 10.20.0.0/16 - eu-west-2"]
    subgraph pub["Public subnets"]
      alb["ALB :443<br/>CloudFront only + secret header"]
      nat["NAT gateway"]
    end
    subgraph priv["Private subnets"]
      subgraph appsg["app security group"]
        api["api"]
        worker["email-worker"]
        fluvio["fluvio-ui"]
        jobs["5 scheduled jobs<br/>+ migrate task"]
      end
      clam["ClamAV :3310"]
      rds[("RDS Postgres 16<br/>6 logical databases")]
      redis[("ElastiCache Redis<br/>TLS")]
    end
  end

  s3u[("S3 uploads x5")]
  sm["Secrets Manager"]
  ecr["ECR"]
  eb["EventBridge Scheduler"]
  net(["Internet<br/>Brevo, Companies House,<br/>Google Indexing, Docker Hub"])

  user -.->|"DNS"| r53
  user -->|"HTTPS"| cfa
  user --> cfm
  cfa -->|"default and /j/*/apply"| s3a
  cfm --> s3m
  cfa -->|"/api /public /j /admin /fluvio"| alb
  alb -->|":8080"| api
  alb -->|"host fluvio.* :80"| fluvio
  api -->|"SQL"| rds
  worker -->|"SQL"| rds
  jobs -->|"SQL"| rds
  api -->|"rate limits"| redis
  api -->|"scan CV"| clam
  api -->|"presign"| s3u
  user -.->|"presigned PUT"| s3u
  priv -->|"egress"| nat
  nat --> net
  sm -.->|"injected at task start"| appsg
  ecr -.->|"image pull"| appsg
  eb -.->|"start jobs"| jobs
```

Staging is stopped and started by hand with `scripts/staging-power.sh` (RDS and services). The NAT gateway, ALB, Redis and WAF keep running.

## Production (home region plus four peered data regions)

The API, workers, Redis and edge run only in the home region. The `global` and `uk` databases share one instance there.
Every other data region has its own VPC, Postgres, uploads bucket and ClamAV scanner, reached over VPC peering.

```mermaid
flowchart LR
  user(["Browsers"])

  subgraph global["Global edge"]
    direction TB
    r53["Route 53 tryopenhr.com"]
    cf["CloudFront app.* and admin.*<br/>+ AWS WAF"]
    fe[("S3 frontends")]
  end

  subgraph home["Home region: eu-west-2 - VPC 10.10.0.0/16"]
    direction TB
    alb["ALB"]
    subgraph tasks["ECS Fargate"]
      api["api"]
      worker["email-worker"]
      fluvio["fluvio-ui"]
      jobs["scheduled jobs + migrate"]
      clamuk["ClamAV (UK)"]
    end
    rdshome[("RDS: openhr_global + openhr_uk")]
    redis[("Redis")]
    s3uk[("S3 uploads: uk")]
    ecr["ECR<br/>replicates openhr/clamav"]
  end

  subgraph sats["Satellite data regions, each peered to the home VPC"]
    direction TB
    subgraph eu["EU: eu-central-1 - 10.11.0.0/16"]
      rdseu[("RDS openhr_eu")]
      s3eu[("S3 uploads: eu")]
      clameu["NLB + ClamAV"]
    end
    subgraph us["US: us-east-1 - 10.12.0.0/16"]
      rdsus[("RDS openhr_us")]
      s3us[("S3 uploads: us")]
      clamus["NLB + ClamAV"]
    end
    subgraph asia["Asia: ap-southeast-1 - 10.13.0.0/16"]
      rdsasia[("RDS openhr_asia")]
      s3asia[("S3 uploads: asia")]
      clamasia["NLB + ClamAV"]
    end
    subgraph africa["Africa: af-south-1 - 10.14.0.0/16 (opt-in region)"]
      rdsafrica[("RDS openhr_africa")]
      s3africa[("S3 uploads: africa")]
      clamafrica["NLB + ClamAV"]
    end
  end

  user -.-> r53
  user --> cf
  cf --> fe
  cf -->|"/api /public /j /admin"| alb
  alb --> api
  alb --> fluvio
  api --> rdshome
  api --> redis
  api --> clamuk
  api --> s3uk
  worker --> rdshome
  jobs --> rdshome

  api -.->|"peering: SQL, scan CV, S3 API"| eu
  api -.-> us
  api -.-> asia
  api -.-> africa
  ecr -.->|"replication"| sats
```

Satellite regions reach the internet through a small NAT (fck-nat instance; a managed gateway in Africa) for ClamAV
signature updates. A CV is stored and scanned in the job's own region, but the single API stack in the home region still
handles the bytes in transit, as it does for all application data. That follows from the one-API-stack topology and
should be stated to counsel when residency commitments are written.

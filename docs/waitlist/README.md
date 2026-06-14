# Waitlist API

HTTP API for waitlist signup and onboarding. All endpoints are versioned under `/api/v1`.

Interactive documentation (Scalar) is available at `/` when the server runs in a non-production environment.

## Domains

| Folder | Description |
|--------|-------------|
| [reference](./reference/) | Reference data for signup and onboarding forms |
| [signup](./signup/) | Initial waitlist registration |
| [onboarding](./onboarding/) | Waitlist onboarding form (pre-launch) |

Product onboarding (post-waitlist) is documented separately in [docs/onboarding/](../onboarding/).

## Base URL

```
/api/v1
```

Example local base: `http://localhost:8080/api/v1`

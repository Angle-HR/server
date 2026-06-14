# Reference

Reference data for waitlist signup and onboarding forms. All endpoints are read-only `GET` requests against the global registry.

Base path: `/api/v1`

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/countries` | Active countries for the region dropdown |
| `GET` | `/industries` | Industry options for onboarding |
| `GET` | `/hiring-tools` | Hiring tool options for onboarding |
| `GET` | `/hiring-frustrations` | Hiring frustration options for onboarding |
| `GET` | `/roles` | Role options for onboarding |
| `GET` | `/team-sizes` | Team size band options for onboarding |

## Example

```bash
curl http://localhost:8080/api/v1/countries
```

Catalog responses include `Cache-Control: public, max-age=300`.

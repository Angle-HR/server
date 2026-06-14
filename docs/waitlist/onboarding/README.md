# Waitlist onboarding

Submits the full waitlist onboarding form for an existing waitlist signup.

This is **not** the same as [product onboarding](../../onboarding/), which will cover post-waitlist flows and is not yet implemented.

## Endpoint

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/waitlist/onboarding` | Submit onboarding form |

Full path: `POST /api/v1/waitlist/onboarding`

## Request body

Requires a valid `waitlist_token` from the signup confirmation email, plus onboarding fields (industry, role, team size, hiring tools, frustrations, and optional free-text fields). See the OpenAPI spec at `/openapi.json` for the full schema.

## Responses

| Status | Meaning |
|--------|---------|
| `201` | Onboarding saved |
| `400` | Invalid request body or validation error |
| `404` | Waitlist token not found |
| `409` | Onboarding already submitted |
| `500` | Server error |

## Example

```bash
curl -X POST http://localhost:8080/api/v1/waitlist/onboarding \
  -H "Content-Type: application/json" \
  -d @onboarding-payload.json
```

# Signup

Registers a user on the regional waitlist and the global users registry.

## Endpoint

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/waitlist` | Join the waitlist |

Full path: `POST /api/v1/waitlist`

## Request body

```json
{
  "full_name": "Jerry",
  "email": "jerry@example.com",
  "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde"
}
```

## Responses

| Status | Meaning |
|--------|---------|
| `201` | Signup created; confirmation email queued |
| `400` | Invalid request body or validation error |
| `409` | Email already registered |
| `500` | Server error |

## Example

```bash
curl -X POST http://localhost:8080/api/v1/waitlist \
  -H "Content-Type: application/json" \
  -d '{"full_name":"Jerry","email":"jerry@example.com","country_id":"a1b2c3d4-e5f6-4789-a012-3456789abcde"}'
```

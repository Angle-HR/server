# Address

Workspace address for the "Make it Yours" step. Supports address search (formatted result) or manual entry.

Base path: `/api/v1/onboarding`

**Status:** Design only — not yet implemented.

## Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT` | `/address` | Bearer JWT | Upsert workspace address |
| `POST` | `/address/verify` | Bearer JWT | Reserved — address provider verification (future) |

## PUT /onboarding/address

### Request (manual entry)

```json
{
  "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde",
  "entry_mode": "manual",
  "line_1": "10 Downing Street",
  "line_2": "",
  "city": "London",
  "state_or_county": "Greater London",
  "post_code": "SW1A 2AA",
  "formatted_address": null
}
```

### Request (search selection)

```json
{
  "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde",
  "entry_mode": "search",
  "line_1": "10 Downing Street",
  "line_2": "",
  "city": "London",
  "state_or_county": "Greater London",
  "post_code": "SW1A 2AA",
  "formatted_address": "10 Downing Street, London SW1A 2AA, UK"
}
```

## Field validation

| Field | Rules |
|-------|-------|
| `country_id` | Required, active country UUID |
| `entry_mode` | Required, `search` or `manual` |
| `line_1` | Required, max 200 chars |
| `line_2` | Optional, max 200 chars |
| `city` | Required, max 100 chars |
| `state_or_county` | Required, max 100 chars |
| `post_code` | Required, max 20 chars |
| `formatted_address` | Required when `entry_mode` is `search`; otherwise null or omitted |

## Response `200`

```json
{
  "data": {
    "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde",
    "entry_mode": "manual",
    "line_1": "10 Downing Street",
    "line_2": "",
    "city": "London",
    "state_or_county": "Greater London",
    "post_code": "SW1A 2AA",
    "formatted_address": null,
    "verification_status": "unverified",
    "onboarding": {
      "current_step": "address",
      "completed_steps": ["verify_email", "profile", "address"],
      "next_step": "business"
    }
  }
}
```

For `individual` accounts, `next_step` is `complete` instead of `business`.

## POST /onboarding/address/verify (reserved)

Third-party address verification is deferred. Until a provider is integrated:

- The UI "Verify address" button should call `PUT /onboarding/address` and proceed.
- This endpoint returns `501 Not Implemented` when called directly.

Future response will set `verification_status` to `verified` or `failed`.

## Errors

| Status | Code | When |
|--------|------|------|
| `400` | `VALIDATION_ERROR` | Missing or invalid fields |
| `400` | `INVALID_REFERENCE` | Unknown `country_id` |
| `401` | `UNAUTHORIZED` | Missing or invalid JWT |
| `500` | `INTERNAL_ERROR` | Server error |

# Session

Onboarding progress introspection and final completion.

Base path: `/api/v1/onboarding`

**Status:** Design only — not yet implemented.

## Endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `GET` | `/status` | Bearer JWT | Current step, completed steps, saved draft fields |
| `POST` | `/complete` | Bearer JWT | Finalize onboarding; create workspace stub |

## GET /onboarding/status

Returns full onboarding state so the client can resume after refresh without guessing.

### Response `200` (business, mid-flow)

```json
{
  "data": {
    "status": "in_progress",
    "account_type": "business",
    "current_step": "address",
    "completed_steps": ["verify_email", "profile"],
    "next_step": "address",
    "profile": {
      "account_type": "business",
      "legal_business_name": "ANGLE",
      "legal_full_name": "Jerry Oluwasegun",
      "company_role_id": "60000000-0000-4000-8000-000000000001"
    },
    "address": null,
    "business": null
  }
}
```

### Response `200` (individual, ready to complete)

```json
{
  "data": {
    "status": "in_progress",
    "account_type": "individual",
    "current_step": "address",
    "completed_steps": ["verify_email", "profile", "address"],
    "next_step": "complete",
    "profile": {
      "account_type": "individual",
      "first_name": "Jerry",
      "last_name": "Oluwasegun",
      "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde"
    },
    "address": {
      "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde",
      "entry_mode": "manual",
      "line_1": "10 Downing Street",
      "city": "London",
      "state_or_county": "Greater London",
      "post_code": "SW1A 2AA",
      "verification_status": "unverified"
    },
    "business": null
  }
}
```

### `status` values

| Value | Meaning |
|-------|---------|
| `in_progress` | Onboarding not yet completed |
| `completed` | `POST /onboarding/complete` succeeded |

## POST /onboarding/complete

Finalizes onboarding after all mandatory steps for the account type are done.

### Request

Empty body.

### Response `201`

```json
{
  "data": {
    "status": "completed",
    "workspace": {
      "id": "770e8400-e29b-41d4-a716-446655440000",
      "slug": "angle"
    },
    "redirect_url": "https://app.openhr.example/dashboard"
  }
}
```

Side effects (implementation phase):

- Sets `onboarding_completed_at` on the user.
- Creates organization/workspace stub for business accounts.
- Enqueues `onboarding_complete` welcome email.

### Completion requirements

| Account type | Required `completed_steps` |
|--------------|---------------------------|
| `individual` | `verify_email`, `profile`, `address` |
| `business` | `verify_email`, `profile`, `address`, `business` |

## Errors

| Status | Code | When |
|--------|------|------|
| `400` | `onboarding_step_incomplete` | Mandatory steps missing |
| `401` | `UNAUTHORIZED` | Missing or invalid JWT |
| `409` | `CONFLICT` | Onboarding already completed |
| `500` | `INTERNAL_ERROR` | Server error |

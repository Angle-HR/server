# Profile

Account type selection and profile fields for the "How will you use Open HR?" step.

Base path: `/api/v1/onboarding`

**Status:** Design only — not yet implemented.

## Endpoint

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT` | `/profile` | Bearer JWT | Upsert account type and profile fields |

## PUT /onboarding/profile

Single upsert endpoint. Validation rules depend on `account_type`.

### Individual request

```json
{
  "account_type": "individual",
  "first_name": "Jerry",
  "last_name": "Oluwasegun",
  "country_id": "a1b2c3d4-e5f6-4789-a012-3456789abcde"
}
```

### Business request

```json
{
  "account_type": "business",
  "legal_business_name": "ANGLE",
  "legal_full_name": "Jerry Oluwasegun",
  "company_role_id": "60000000-0000-4000-8000-000000000001"
}
```

## Field validation

| Field | Individual | Business | Rules |
|-------|------------|----------|-------|
| `account_type` | required | required | `individual` or `business` |
| `first_name` | required | — | max 80 chars |
| `last_name` | required | — | max 80 chars |
| `country_id` | required | — | active country UUID |
| `legal_business_name` | — | required | max 200 chars |
| `legal_full_name` | — | required | max 200 chars |
| `company_role_id` | — | required | active `company_roles` UUID |

Fields not applicable to the chosen `account_type` must be omitted or null. Sending business-only fields for `individual` (or vice versa) returns `400` with field-level details.

## Response `200`

```json
{
  "data": {
    "account_type": "business",
    "legal_business_name": "ANGLE",
    "legal_full_name": "Jerry Oluwasegun",
    "company_role_id": "60000000-0000-4000-8000-000000000001",
    "region": "uk",
    "onboarding": {
      "current_step": "profile",
      "completed_steps": ["verify_email", "profile"],
      "next_step": "address"
    }
  }
}
```

`region` is inferred from `country_id` (individual) or default region assignment for business accounts on first profile save, and written to `users_registry`.

## Errors

| Status | Code | When |
|--------|------|------|
| `400` | `VALIDATION_ERROR` | Missing or invalid fields |
| `400` | `INVALID_REFERENCE` | Unknown `country_id` or `company_role_id` |
| `401` | `UNAUTHORIZED` | Missing or invalid JWT |
| `500` | `INTERNAL_ERROR` | Server error |

## Account type changes

Switching `account_type` after initial save clears fields and progress for the other branch:

- `individual` → `business`: clears `first_name`, `last_name`; resets `business` step if previously saved.
- `business` → `individual`: clears organization profile fields; resets `business` step.

The client should confirm with the user before switching types mid-flow.

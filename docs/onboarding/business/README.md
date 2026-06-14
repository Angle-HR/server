# Business compliance

The "One Last step" screen for business accounts. Collects business type, industry, and employee count for compliance setup.

Base path: `/api/v1/onboarding`

**Status:** Design only — not yet implemented.

## Endpoint

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `PUT` | `/business` | Bearer JWT | Upsert business compliance fields |

**Business accounts only.** Individual accounts must not call this endpoint.

## PUT /onboarding/business

### Request

```json
{
  "business_type_id": "61000000-0000-4000-8000-000000000001",
  "industry_id": "62000000-0000-4000-8000-000000000001",
  "employee_count": 25
}
```

## Field validation

| Field | Rules |
|-------|-------|
| `business_type_id` | Required, active `business_types` UUID |
| `industry_id` | Required, active `onboarding_industries` UUID |
| `employee_count` | Required, integer 1–10000 |

`employee_count` is a free-form number (as shown in the UI), not the waitlist `team_sizes` bands.

## Response `200`

```json
{
  "data": {
    "business_type_id": "61000000-0000-4000-8000-000000000001",
    "industry_id": "62000000-0000-4000-8000-000000000001",
    "employee_count": 25,
    "onboarding": {
      "current_step": "business",
      "completed_steps": ["verify_email", "profile", "address", "business"],
      "next_step": "complete"
    }
  }
}
```

## Errors

| Status | Code | When |
|--------|------|------|
| `400` | `VALIDATION_ERROR` | Missing or invalid fields |
| `400` | `INVALID_REFERENCE` | Unknown catalog UUID |
| `400` | `invalid_account_type_branch` | `account_type` is `individual` |
| `401` | `UNAUTHORIZED` | Missing or invalid JWT |
| `500` | `INTERNAL_ERROR` | Server error |

## Catalog references

Load options from [reference](./../reference/) endpoints before submitting:

- `GET /onboarding/business-types`
- `GET /onboarding/industries`

Countries and company roles are collected in earlier steps.

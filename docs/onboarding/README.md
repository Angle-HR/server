# Onboarding API

Product onboarding for users after waitlist signup. This is **separate** from [waitlist onboarding](../waitlist/onboarding/), which collects the pre-launch interest form at `POST /api/v1/waitlist/onboarding`.

Endpoints for this section are **not yet implemented**. They will appear under `/api/v1` and in the Scalar sidebar under **Onboarding** as handlers are added.

## Planned structure

Subfolders can be added here as domains emerge, for example:

```
docs/onboarding/
  README.md
  <domain>/README.md
```

## Adding endpoints

When implementing a new onboarding handler:

1. Use `@Tags onboarding/<domain>` in handler godoc (e.g. `onboarding/profile`).
2. Add the tag to the **Onboarding** group in [`internal/docs/taggroups.go`](../../internal/docs/taggroups.go).
3. Add a tag definition with `x-displayName` in the same file.
4. Document the endpoint under `docs/onboarding/<domain>/`.
5. Run `make swagger` to regenerate the OpenAPI spec.

## Base URL

```
/api/v1
```

Example local base: `http://localhost:8080/api/v1`

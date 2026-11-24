// Command apidocs exists only to give swag (make swagger) the OpenAPI general information and, through its imports,
// every package that carries route annotations. It is not built into any image.
//
//	@title						OpenHR API
//	@version					1.0
//	@description				Product API for OpenHR: accounts, onboarding, hiring and applications.
//	@description				Interactive docs (Scalar) are served at `/` in non-production environments.
//	@description				Product flow: signup → verify email (6-digit OTP, 5 min expiry) → profile → address → [business] → complete.
//	@description				Authentication is a server-side session in an HttpOnly cookie (`__Host-openhr_session`, or `openhr_session` when COOKIE_SECURE=false); there are no bearer tokens.
//	@description				Login, verify-email, login/otp/verify, login/totp and accept-invite return `csrf_token` in the body; `GET /auth/me` returns it again on page load. Every POST/PUT/PATCH/DELETE on an authenticated route must send it as `X-CSRF-Token` (403 `CSRF_INVALID` otherwise). Browsers must call with credentials included.
//	@description				Admin ops console APIs live under `/api/v1/admin` and use a separate cookie (`__Host-openhr_admin_session`) and CSRF token.
//	@host						localhost:8081
//	@BasePath					/api/v1
//
//	@securityDefinitions.apikey	SessionCookie
//	@in							header
//	@name						Cookie
//	@description				Product session cookie (`__Host-openhr_session`). Set by the server on login; sent automatically by browsers. Unsafe methods also need the `X-CSRF-Token` header.
//
//	@securityDefinitions.apikey	AdminSessionCookie
//	@in							header
//	@name						Cookie
//	@description				Admin session cookie (`__Host-openhr_admin_session`). Unsafe methods also need the `X-CSRF-Token` header.
package main

import (
	_ "github.com/Angle-HR/server/internal/handler"
	_ "github.com/Angle-HR/server/internal/hiring/apphttp"
	_ "github.com/Angle-HR/server/internal/hiring/hiringhttp"
	_ "github.com/Angle-HR/server/internal/identityhttp"
	_ "github.com/Angle-HR/server/internal/orghttp"
)

func main() {}

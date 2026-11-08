package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/mrhollen/ravn/internal/application"
	"github.com/mrhollen/ravn/internal/domain"
)

// ---- DTOs ------------------------------------------------------------------

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type identityDTO struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	AuthMethod string `json:"auth_method"`
	CreatedAt  string `json:"created_at"`
}

type loginResp struct {
	Token     string      `json:"token"` // opaque session secret, shown exactly once (only its hash is stored)
	ExpiresAt string      `json:"expires_at"`
	Identity  identityDTO `json:"identity"`
}

type sessionResp struct {
	Identity  identityDTO `json:"identity"`
	ExpiresAt string      `json:"expires_at"`
}

type accountReq struct {
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"` // "human" (default) | "agent"
	Password string `json:"password"`       // min 8 chars
}

type accountResp struct {
	Identity identityDTO `json:"identity"`
	Created  bool        `json:"created"` // true → HTTP 201; false (reset) → 200
}

type tokenMintReq struct {
	Name  string `json:"name"`           // target identity (created as neutral if absent)
	Kind  string `json:"kind,omitempty"` // "human" (default) | "agent"
	Label string `json:"label,omitempty"`
}

type apiTokenRow struct {
	ID         string  `json:"id"`
	IdentityID string  `json:"identity_id"`
	Label      string  `json:"label,omitempty"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	Disabled   bool    `json:"disabled"`
}

type tokenMintResp struct {
	Token string      `json:"token"` // opaque secret, shown exactly once
	Meta  apiTokenRow `json:"meta"`  // non-secret metadata
}

// ---- small converters ------------------------------------------------------

func identityDTOFrom(id domain.Identity) identityDTO {
	return identityDTO{
		ID:         string(id.ID),
		Name:       id.Name,
		Kind:       string(id.Kind),
		AuthMethod: string(id.AuthMethod),
		CreatedAt:  rfc3339(id.CreatedAt),
	}
}

func apiTokenRowFrom(m application.APITokenMeta) apiTokenRow {
	return apiTokenRow{
		ID:         string(m.ID),
		IdentityID: string(m.IdentityID),
		Label:      m.Label,
		CreatedAt:  rfc3339(m.CreatedAt),
		LastUsedAt: tsOrNil(m.LastUsedAt),
		Disabled:   m.Disabled,
	}
}

func tsOrNil(p *time.Time) *string {
	if p == nil {
		return nil
	}
	s := rfc3339(*p)
	return &s
}

// requireAuthEnabled gates every /auth/* handler on the auth use case being wired. When a
// deployment has no database (nil Auth) the routes are registered but inert → 404, matching
// how missing optional features surface elsewhere.
func (a *App) requireAuthEnabled(w http.ResponseWriter) bool {
	if a.Auth != nil {
		return true
	}
	writeJSONError(w, http.StatusNotFound, map[string]any{
		"error":   ErrCodeNotFound,
		"message": "authentication is not enabled",
	})
	return false
}

// requireOperator enforces that the presenting principal is a shared-operator (token mode or
// multiuser RAVN_TOKEN holder). Credential principals (session/api_token) are rejected with
// 403 so per-user tokens can never provision accounts or mint other tokens.
func requireOperator(r *http.Request) bool {
	p, _ := application.PrincipalFromContext(r.Context())
	return p.IsOperator
}

// ---- handlers --------------------------------------------------------------

// handleAuthLogin: POST /api/v1/auth/login (public in multiuser). Exchanges a username +
// password for a fresh session token. Wrong/unknown/disabled credentials → 401 "invalid
// username or password" (indistinguishable, so usernames can't be enumerated); an empty
// username → 400 via domain.ErrInvalid.
func (a *App) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	var req loginReq
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, application.ErrAuthCredentialsInvalid) {
			writeJSONError(w, http.StatusUnauthorized, map[string]any{
				"error":   ErrCodeUnauthorized,
				"message": "invalid username or password",
			})
			return
		}
		writeAppError(w, err, "auth", "")
		return
	}
	writeJSON(w, http.StatusOK, loginResp{Token: res.Token, ExpiresAt: rfc3339(res.ExpiresAt), Identity: identityDTOFrom(res.Identity)})
}

// handleAuthSession: GET /api/v1/auth/session. Inspects the presenting session bearer → its
// enabled identity + expiry. No active session for this token → 401. (A shared-operator or
// api_token bearer has no session; clients should use /auth/login then present that session.)
func (a *App) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	tok := primaryBearer(r)
	if tok == "" {
		writeJSONError(w, http.StatusUnauthorized, map[string]any{
			"error":   ErrCodeUnauthorized,
			"message": "missing or invalid auth token",
		})
		return
	}
	id, exp, err := a.Auth.InspectSession(r.Context(), tok)
	if err != nil {
		if errors.Is(err, application.ErrAuthNoSession) {
			writeJSONError(w, http.StatusUnauthorized, map[string]any{
				"error":   ErrCodeUnauthorized,
				"message": "no active session for this token",
			})
			return
		}
		writeAppError(w, err, "auth", "")
		return
	}
	writeJSON(w, http.StatusOK, sessionResp{Identity: identityDTOFrom(id), ExpiresAt: rfc3339(exp)})
}

// handleAuthSessionRevoke: DELETE /api/v1/auth/session. Invalidates the presenting session
// bearer. Idempotent → 204 for a valid, unknown, or already-revoked token alike (a replayed
// logout never errors).
func (a *App) handleAuthSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	if tok := primaryBearer(r); tok != "" {
		if err := a.Auth.RevokeSession(r.Context(), tok); err != nil {
			writeAppError(w, err, "auth", "")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent) // idempotent success
}

// handleAuthAccountCreate: POST /api/v1/auth/accounts (operator-only). Creates a human
// identity with a password credential (or resets the password of an existing one). 201 on
// create, 200 on reset. Empty name / short password / bad kind → 400.
func (a *App) handleAuthAccountCreate(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	if !requireOperator(r) {
		writeJSONError(w, http.StatusForbidden, map[string]any{
			"error":   ErrCodeForbidden,
			"message": "operator access required",
		})
		return
	}
	var req accountReq
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.Auth.CreateAccount(r.Context(), req.Name, domain.ActorType(req.Kind), req.Password)
	if err != nil {
		writeAppError(w, err, "auth", "")
		return
	}
	code := http.StatusOK
	if res.Created {
		code = http.StatusCreated
	}
	writeJSON(w, code, accountResp{Identity: identityDTOFrom(res.Identity), Created: res.Created})
}

// handleAuthTokenMint: POST /api/v1/auth/tokens (operator-only). Mints an API token for the
// named identity (creating it as a neutral row first if absent). Returns the opaque secret
// once plus its non-secret metadata → 201.
func (a *App) handleAuthTokenMint(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	if !requireOperator(r) {
		writeJSONError(w, http.StatusForbidden, map[string]any{
			"error":   ErrCodeForbidden,
			"message": "operator access required",
		})
		return
	}
	var req tokenMintReq
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := a.Auth.MintAPIToken(r.Context(), req.Name, domain.ActorType(req.Kind), req.Label)
	if err != nil {
		writeAppError(w, err, "auth", "")
		return
	}
	writeJSON(w, http.StatusCreated, tokenMintResp{Token: res.Token, Meta: apiTokenRowFrom(res.Meta)})
}

// handleAuthTokenList: GET /api/v1/auth/tokens?name= (operator-only). Lists the non-secret
// metadata of every API token for the named identity (disabled rows included). Unknown
// identity → 404; none → empty array.
func (a *App) handleAuthTokenList(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	if !requireOperator(r) {
		writeJSONError(w, http.StatusForbidden, map[string]any{
			"error":   ErrCodeForbidden,
			"message": "operator access required",
		})
		return
	}
	rows, err := a.Auth.ListAPITokens(r.Context(), r.URL.Query().Get("name"))
	if err != nil {
		writeAppError(w, err, "auth", "")
		return
	}
	out := make([]apiTokenRow, 0, len(rows))
	for _, m := range rows {
		out = append(out, apiTokenRowFrom(m))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAuthTokenRevoke: DELETE /api/v1/auth/tokens/{id} (operator-only). Disables an API
// token by durable id → 204. Unknown/empty id → 404/400; already-disabled is a no-op 204.
func (a *App) handleAuthTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if !a.requireAuthEnabled(w) {
		return
	}
	if !requireOperator(r) {
		writeJSONError(w, http.StatusForbidden, map[string]any{
			"error":   ErrCodeForbidden,
			"message": "operator access required",
		})
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.Auth.RevokeAPIToken(r.Context(), id); err != nil {
		writeAppError(w, err, "auth", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

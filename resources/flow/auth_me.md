[← Back to Master Flow Catalog](../FLOW.md)

# Flow: Current User Profile (`GET /api/auth/me`)

This document describes how the current authenticated user identity is verified and retrieved in **`godocs`**.

---

## 1. Overview & Endpoint Contract

- **HTTP Method & Path**: `GET /api/auth/me`
- **Authentication Required**: Yes (`session_id` cookie)
- **Response Content-Type**: `application/json`

### Response Body (`200 OK`)
```json
{
  "id": "0191c49b-73a2-71c1-90a8-a5b8b6e680a1",
  "email": "learner@example.com",
  "created_at": "2026-09-10T10:00:00Z"
}
```

---

## 2. Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    actor Client as HTTP Client / Browser
    participant Handler as auth.Handler.me
    participant Svc as auth.Service.Authenticate
    participant Repo as db.AuthRepo
    participant DB as SQLite DB

    Client->>Handler: GET /api/auth/me (Cookie: session_id=<token>)

    Handler->>Handler: r.Cookie("session_id")
    alt Missing Cookie
        Handler-->>Client: 401 Unauthorized (code: "unauthorized", message: "authentication required")
    end

    Handler->>Svc: Authenticate(ctx, token)
    Svc->>Repo: GetSession(ctx, token)
    Repo->>DB: SELECT token, user_id, expires_at FROM sessions WHERE token = ?
    alt Session Not Found
        DB-->>Repo: sql.ErrNoRows
        Repo-->>Svc: ErrNoSession
        Svc-->>Handler: ErrNoSession
        Handler-->>Client: 401 Unauthorized (code: "unauthorized", message: "invalid or expired session")
    else Session Found
        DB-->>Repo: Session Record
        Repo-->>Svc: Session
    end

    Note over Svc: Expiration Verification
    Svc->>Svc: Verify expires_at > time.Now()
    alt Session Expired
        Svc-->>Handler: ErrNoSession
        Handler-->>Client: 401 Unauthorized (code: "unauthorized")
    end

    Svc->>Repo: GetUserByID(ctx, session.UserID)
    Repo->>DB: SELECT id, email, created_at FROM users WHERE id = ?
    DB-->>Repo: User Record
    Repo-->>Svc: User
    Svc-->>Handler: User Profile
    Handler-->>Client: 200 OK {id, email, created_at}
```

---

## 3. Step-by-Step Processing Pipeline

1. **Cookie Extraction**:
   Reads `session_id` from the HTTP request headers. If absent, immediately returns `401 Unauthorized`.
2. **Session Lookup**:
   Queries SQLite `sessions` table for the matching token string.
3. **Validity Check**:
   Compares `expires_at` against the current UTC timestamp `time.Now()`. Expired sessions return `401`.
4. **User Profile Retrieval**:
   Executes `GetUserByID` to load the current profile fields (`id`, `email`, `created_at`).
5. **Response Serialization**:
   Returns the user domain entity formatted as JSON.

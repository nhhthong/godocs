# Flow: User Logout (`POST /api/auth/logout`)

This document details the session invalidation and cookie clearing flow in **`godocs`**.

---

## 1. Overview & Endpoint Contract

- **HTTP Method & Path**: `POST /api/auth/logout`
- **Authentication Required**: Yes (expects `session_id` cookie, but gracefully handles missing/expired cookies)
- **Response**: `204 No Content` with an expired Set-Cookie header.

### Request Headers
```http
POST /api/auth/logout HTTP/1.1
Cookie: session_id=0191c49b-73a2-71c1-90a8-a5b8b6e680a1
```

### Response Headers (`204 No Content`)
```http
HTTP/1.1 204 No Content
Set-Cookie: session_id=; Path=/; Max-Age=-1
```

---

## 2. Sequence Diagram

```mermaid
sequenceDiagram
    autonumber
    actor Client as HTTP Client / Browser
    participant Handler as auth.Handler.logout
    participant Svc as auth.Service.Logout
    participant Repo as db.AuthRepo
    participant DB as SQLite DB

    Client->>Handler: POST /api/auth/logout (with Cookie: session_id)
    
    alt Cookie Present
        Handler->>Handler: Extract token from r.Cookie("session_id")
        Handler->>Svc: Logout(ctx, token)
        Svc->>Repo: DeleteSession(ctx, token)
        Repo->>DB: DELETE FROM sessions WHERE token = ?
        DB-->>Repo: Deleted
        Repo-->>Svc: nil
        Svc-->>Handler: nil
    else Cookie Absent or Malformed
        Note over Handler: Silently continue to ensure client cookie is wiped
    end

    Note over Handler: Browser Cookie Erasure
    Handler->>Handler: Set-Cookie: session_id=; Path=/; Max-Age=-1
    Handler-->>Client: 204 No Content
```

---

## 3. Step-by-Step Processing Pipeline

1. **Cookie Inspection**:
   The handler checks for the `session_id` cookie in the incoming request headers.
2. **Server-Side Session Deletion**:
   If a cookie exists, the service executes `DELETE FROM sessions WHERE token = ?` in SQLite. This ensures instantaneous revocation on the backend, unlike stateless JWTs which remain valid until expiry.
3. **Idempotency & Fault Tolerance**:
   If the token is already expired, missing, or invalid, the handler does not throw an error. The goal is to always leave the client in an unauthenticated state.
4. **Browser Cookie Purge**:
   Issues a `Set-Cookie` header with `Max-Age=-1` and empty value, instructing modern web browsers to immediately purge the cookie from disk and memory.
5. **Zero-Body Response**:
   Returns HTTP `204 No Content`.

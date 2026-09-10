[← Back to the flow index](../FLOW.md)

# Flow: Logout (`POST /api/auth/logout`)

Deletes the session row and clears the cookie. Safe to call even without a valid
session — the goal is just to end up logged out.

---

## 1. Contract

- **Method & path**: `POST /api/auth/logout`
- **Auth**: reads the `godocs_session` cookie if present, but doesn't require a valid one
- **Response**: `204 No Content`, plus a `Set-Cookie` that expires the cookie

```http
POST /api/auth/logout HTTP/1.1
Cookie: godocs_session=<token>
```

```http
HTTP/1.1 204 No Content
Set-Cookie: godocs_session=; Path=/; Max-Age=-1
```

---

## 2. Sequence

```mermaid
sequenceDiagram
    autonumber
    actor Client
    participant H as auth.Handler.logout
    participant S as auth.Service.Logout
    participant R as db.AuthRepo
    participant DB as SQLite

    Client->>H: POST /api/auth/logout (Cookie: godocs_session)

    alt cookie present
        H->>S: Logout(ctx, token)
        S->>R: DeleteSession(ctx, token)
        R->>DB: DELETE FROM sessions WHERE token = sha256(token)
        DB-->>R: done (0 or 1 rows)
    else no cookie
        Note over H: nothing to delete — keep going
    end

    H->>H: Set-Cookie godocs_session=; Max-Age=-1
    H-->>Client: 204 No Content
```

---

## 3. Step by step

1. **Read the cookie.** If it's missing or malformed, skip straight to step 3.
2. **Delete the row.** `DELETE FROM sessions WHERE token = ?`, using
   `sha256(token)` because that's what login stored. Deleting a token that isn't
   there is not an error — the statement just affects 0 rows. This is what makes
   server-side sessions revocable: unlike a JWT, the session stops working the
   moment the row is gone.
3. **Expire the cookie.** `Set-Cookie` with an empty value and `Max-Age=-1` tells
   the browser to drop it now.
4. **Return `204`** — success, no body.

# ADR 0002: Opaque cookie sessions and CSRF defense

## Problem

Local accounts need secure authentication without exposing credentials or reusable bearer tokens to JavaScript.

## Options

Use JWTs in browser storage, server-side opaque sessions, or HTTP basic authentication.

## Trade-offs

JWTs complicate revocation and browser storage raises XSS exposure. Server-side sessions require a small database table but support logout and rotation.

## Decision

Use randomly generated opaque session tokens in HttpOnly, SameSite=Strict cookies. Only a SHA-256 token digest is stored. Session IDs rotate on login; state-changing authenticated requests require a per-session CSRF header and same-origin validation.

## Consequences

HTTPS enables the Secure cookie attribute automatically when TLS is configured. Plain HTTP is intended only for local development; production deployments should terminate TLS.

# ADR-0002: Go Backend

**Status:** Accepted

## Decision
Use Go as primary backend/worker/agent language. Prefer standard library plus small focused dependencies.

## Rationale
Fast, simple Linux/Windows binaries; low runtime overhead; explicit code; consistent formatting; limited language/framework surface is advantageous for long-lived AI maintenance. Rust remains a possible specialized future choice only with a new ADR.

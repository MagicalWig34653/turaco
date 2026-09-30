---
name: release
description: Prepare and validate a Turaco release without changing product behavior.
disable-model-invocation: true
model: claude-sonnet-5-5
effort: medium
---
# Release

1. Ensure working tree is expected and release branch/tag intent is explicit.
2. Run `make check`.
3. Review migrations and agent compatibility notes.
4. Confirm generated docs and changelog/release notes are current.
5. Run `/release-readiness` for significant releases.
6. Do not push, tag or publish without explicit user authorization.
7. Production releases use immutable image digests; never rely on `latest`.

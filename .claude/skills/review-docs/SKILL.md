---
name: review-docs
description: Verify that Turaco documentation accurately matches implementation and generated references.
model: claude-sonnet-5-5
effort: medium
---
# Documentation Review

Compare the change to authoritative docs. Identify claims that are stale, implementation behavior that lacks required documentation, duplicate facts likely to drift, broken links and missing ADRs. Run `make docs-check`.

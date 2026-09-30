# Colima Profile for Apple Silicon

The recommended local model is native Go/Node/Claude Code plus containerized stateful dependencies.

Suggested starting profile for an M4 Pro with 48 GB RAM:

```bash
colima start --cpu 6 --memory 12 --disk 80
```

This is a starting point, not an application requirement. Increase resources only if later integration tests justify it.

The repository uses standard Docker CLI/Compose commands and must not depend on Colima-specific APIs. This keeps the same developer workflow usable with Docker Desktop, Rancher Desktop, Linux Docker or CI.

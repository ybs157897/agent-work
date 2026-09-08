# Workflow

- Before building or adapting, research and benchmark reference open-source projects: evaluate their architecture, read their source, and study their rendering/interaction details (e.g., kanna, kimi-code, nanobrowser, paperclip, codex desktop). Confidence: 0.85
- Prefers verifying web UI behavior in a real browser rather than guessing from code alone. Confidence: 0.85
- Likes plan-first execution: write architecture/design docs and an implementation plan, then implement strictly per the plan without editing the plan file. Confidence: 0.75
- Installs and uses community skills for UI/web optimization; rejects paid tools — prefers free and open-source options. Confidence: 0.7
- Git: commit code first and large binary files last, in separate commits. Confidence: 0.6
- Long-running local services (e.g., a subscription proxy reused by other tools) should run in the background under a daemon/supervisor so they keep serving after the client app (Cursor/IDE) closes. Confidence: 0.8
- When verifying/testing existing functionality: no code modifications — start/run it and record anomaly points and steps only; tests must use real models and real personas, not mocks. Confidence: 0.7

# Engineering

- No silent compatibility layers or fallbacks: if a config file lacks a required entry, fail explicitly — but surface a clear, well-formatted error message instead of inventing behavior. Confidence: 0.9
- No runtime fallback: option sets (approval strategies, sandbox, tools, modes) must come from the selected runtime and be fetched dynamically from its APIs; only show what that runtime actually supports. Confidence: 0.9
- Store real API keys directly in local config files; do not require environment variables for a local tool. Confidence: 0.85
- One config file per provider holding multiple models, rather than one file per model. Confidence: 0.85
- Unify per-runtime configuration in a project-space directory (e.g., .agent-work) that is reused across runtimes (codex, kimi, deepseek harness) instead of each runtime's own config dir. Confidence: 0.8
- Prefer fixed standalone runtime binaries committed to the repo so other machines don't re-download; do not mix with environment-installed CLIs; no login required. Confidence: 0.8
- Operations that can hang (e.g., loading) need a time limit with a clear error prompt; add logging first, refine per-error messaging later. Confidence: 0.75
- Empty optional fields (e.g., max output tokens) should fall back to the model's own defaults. Confidence: 0.7

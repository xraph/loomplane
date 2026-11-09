# Loomplane: keep AI working files out of git

Plans, specs and session notes stay on your machine. `.gitignore` excludes `docs/superpowers/`, `docs/qualification/`, `docs/plan.md`, `docs/one-click-services-plan.md`, `docs/catalog-recovery-verification.md`, `.superpowers/` and `.Codex/`.

- Write superpowers specs and plans where the skill says, but don't commit them. Skip the skill's "commit the spec" step.
- Never `git add -f` an ignored path. The "Ignored files" workflow fails any PR or push that tracks one.
- Don't link to these files from tracked docs. If a reader needs something from one, put it in the doc itself.
- Put verification notes in the PR description or the commit body, not in a new file under `docs/`.

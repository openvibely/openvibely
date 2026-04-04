# OpenVibely — Project Guide

## STOP — Read These Rules FIRST Before Writing ANY Code

**NEVER create documentation/summary files.** No `*_FIX.md`, `*_SUMMARY.md`, `*_VERIFICATION.md`, `README_*.md`, `TECHNICAL_*.md`, `ACTION_PLAN_*.md`, `FINDINGS_*.md`, `INVESTIGATION_*.md`, `COMMIT_MESSAGE.txt`, or ANY other markdown files that describe/summarize/document your changes. The code and commit messages ARE the documentation. If you catch yourself thinking "let me create a summary document" — STOP.

**NEVER run `go build` or `go test` more than once per task.** Make ALL code changes first, then run the build+test command chain exactly ONCE at the end. If it fails, fix and run once more. Maximum 2 runs total. Do NOT run tests after each file, do NOT run subsets then full suite, do NOT run "verification" builds.

---

You MUST read these files before doing ANY work. Do not skip this step. Do not assume you can answer without them.

1. @MEMORY.md — **Project context**: architecture, provider layout, shared patterns, page routes. Orient yourself here.
2. @guardrails.md — **Pitfall prevention**: rules that prevent repeated bugs and mistakes. Every entry is a past mistake or known trap.
3. @PRACTICES.md — **Project practices**: high-level development workflow and coding conventions for this repository.

Keep these files distinct:
- `MEMORY.md` = project context and current architecture/feature behavior
- `guardrails.md` = concrete pitfalls and "never do this" bug-prevention rules
- `PRACTICES.md` = reusable high-level practices only

Do not put feature-specific implementation notes in `PRACTICES.md` (for example plugin flows, one endpoint's behavior, or model/provider-specific edge cases). Put those in `MEMORY.md` (context) or `guardrails.md` (pitfall prevention).

## Critical Rules

- **Always create or update tests when fixing bugs or adding features.** Every fix must have a corresponding test.
- Run `go test ./internal/... -count=1 -timeout 60s` after making changes
- Run `templ generate` after modifying any `.templ` file
- Never change `busy_timeout` or `MaxOpenConns` in `database.go`
- Strip `CLAUDECODE` env var when spawning Claude CLI subprocess
- Use `TaskRepo.ClaimTask()` for atomic task claiming (never set status to running directly)
- Use parameterized queries (`?` placeholders) for all SQL
- Respect FK constraints in test data — create referenced records first

## Making Changes

- Follow the layered architecture: models → repository → service → handler → templates
- Use raw SQL in repositories (no ORM). Use `QueryRowContext` with `RETURNING` for inserts
- Use `context.Context` for all database and service calls

## Adding Features

- New tables/columns → new migration in `internal/database/migrations/` (numbering: `004_description.sql`)
- New models → `internal/models/`, repos → `internal/repository/`, services → `internal/service/`, handlers → `internal/handler/`
- Register new handlers in `handler.go`

## Testing

- Use `testutil.NewTestDB(t)` for all DB tests (fresh in-memory DB per test)
- Never `t.Parallel()` with shared database connections
- Use valid CHECK constraint values for fixtures (see guardrails.md)
- Bug fixes require a test that reproduces the bug first

## Running

```bash
./start.sh              # Start server (logs to logs/openvibely.log)
make dev                # Development with live reload
go test ./internal/... -count=1 -timeout 60s  # Tests
```

## Key Files

| What | Where |
|------|-------|
| Entry point | `cmd/server/main.go` |
| Database setup | `internal/database/database.go` |
| Migrations | `internal/database/migrations/*.sql` |
| Models | `internal/models/*.go` |
| Repositories | `internal/repository/*_repo.go` |
| Services | `internal/service/*_service.go` |
| HTTP Handlers | `internal/handler/*_handler.go` |
| Route registration | `internal/handler/handler.go` |
| Templates | `web/templates/**/*.templ` |
| Test helper | `internal/testutil/testdb.go` |

## Memory Maintenance

Update `MEMORY.md`, `guardrails.md`, and `PRACTICES.md` after completing work that adds features, encounters pitfalls, or introduces new reusable practices.
- Keep `PRACTICES.md` focused on high-level project practices.
- Move one-off feature specifics and incident-level details to `MEMORY.md` or `guardrails.md`.
- Condense periodically — remove stale entries rather than leaving misleading guidance.

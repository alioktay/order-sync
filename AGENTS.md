# AGENTS.md

This repository owns PostgreSQL and order-sync only. The dashboard, mock-SAP, and Adminer live in the sibling `C:\work\projects\order-sync-helper` repository.

## Codebase discovery

This project uses `codebase-memory-mcp` to maintain a knowledge graph.
Always prefer MCP graph tools over grep, glob, or file search for code discovery.

Use these tools in order:

1. `search_graph` - find functions, classes, routes, and variables by pattern.
2. `trace_path` - trace callers or callees.
3. `get_code_snippet` - read a specific function or class.
4. `query_graph` - run Cypher queries for complex patterns.
5. `get_architecture` - inspect the high-level project structure.

Use grep or file search for string literals, error messages, configuration values,
and non-code files such as SQL, Dockerfiles, and shell scripts.

If the repository is not indexed, run `index_repository` before using graph tools.

## Schema and migrations

This is a fresh project. The current schema is defined directly in
`migrations/001_initial.sql`.

- Do not preserve backward compatibility with previous schemas unless explicitly requested.
- Do not add accumulating or numbered incremental migrations.
- For schema changes, edit the current canonical schema directly.
- Do not add compatibility `ALTER TABLE`, legacy cleanup, or data-preservation logic for old deployments unless explicitly requested.

## Docker Compose validation

After every code or schema change, refresh the Compose database, then build and
deploy the affected module through Docker Compose before considering the change
complete:

```bash
docker compose down -v
docker compose up -d --build <service>
```

The `-v` flag is required: every deployment must start with a fresh database.
This intentionally removes the Compose PostgreSQL volume and all local data.
Use the affected Compose service: `order-sync` (PostgreSQL is shared infrastructure).
For shared schema or Compose changes, rebuild and deploy all affected services.
If Docker Compose cannot be run because required environment or infrastructure is
unavailable, report that explicitly along with the exact command that was not run.

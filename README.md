# Agent Skills

`agent-skills` is a personal collection of reusable Agent Skills backed by
deterministic tools. Each installable unit lives at `skills/<skill-name>` and
contains its own instructions, references, launcher, implementation, and tests.

Status: **Public preview**

## Available Skill

- `shopify-media-sync` — plans, previews, executes, inspects, and safely
  recovers Shopify Files media synchronization.

## Install

Install the Skill into a project with the pinned Skills CLI:

```bash
npx skills@1.5.21 add JulCyan/agent-skills --skill shopify-media-sync --copy
```

List the Skills exposed by the repository:

```bash
npx skills@1.5.21 add JulCyan/agent-skills --list
```

The consumer project owns the generated `skills-lock.json`. This provider
repository intentionally does not commit a root lockfile because it publishes
Skills rather than consuming them.

## Runtime

The installed Skill selects an executor in this order:

1. an explicit trusted `SHOPIFY_MEDIA_SYNC_BINARY`;
2. a fixed Release asset whose SHA-256 matches `runtime.json`;
3. the bundled Go implementation when Go is available;
4. a structured `NEEDS_SETUP` result.

The current preview has no binary Release, so systems without Go fail closed.
Optional Lark/Feishu inputs require `lark-cli`; local CSV, XLSX, JSON, zip, and
directory inputs do not.

## Safety

- Planning and preview do not write to Shopify.
- Remote execution requires an explicit reviewed plan and `--execute`.
- Credentials are read only from explicit supported configuration and are
  never printed.
- The repository uses synthetic fixtures and scans for credentials, absolute
  personal paths, real-looking target IDs, and operator-supplied prohibited
  identifiers.

## Development

Requirements:

- Node.js 20.19 or newer
- the Go version declared in
  `skills/shopify-media-sync/scripts/media-sync-go/go.mod`

Run all checks:

```bash
npm test
```

OpenSpec artifacts under `openspec/` define repository changes. Product Skills
live only under `skills/`; local agent helpers are ignored and are not
installable products.

See [publication status](PUBLICATION_STATUS.md),
[installation verification](docs/verification.md), and
[maintenance rules](docs/maintenance.md).

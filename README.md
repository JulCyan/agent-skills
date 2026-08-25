# Agent Skills

`agent-skills` is a personal collection of reusable Agent Skills backed by
deterministic tools. Each installable unit lives at `skills/<skill-name>` and
contains its own instructions, references, launcher, implementation, and tests.

Status: **Public preview**

## Available Skills

- `shopify-media-sync` — plans, previews, executes, inspects, and safely
  recovers Shopify Files media synchronization.
- `theme-template-sync` — plans, previews, and executes scoped Shopify JSON
  template synchronization with canonical readback.

## Install

Install one Skill into a project with the current Skills CLI:

```bash
npx skills add JulCyan/agent-skills --skill shopify-media-sync --copy
npx skills add JulCyan/agent-skills --skill theme-template-sync --copy
```

List the Skills exposed by the repository:

```bash
npx skills add JulCyan/agent-skills --list
```

This public preview does not yet include an open-source license. Until a
license is selected, the commands above are for the author's own acceptance or
other explicitly authorized evaluation; source visibility is not a grant to
copy, modify, or redistribute the project.

The consumer project owns the generated `skills-lock.json`. This provider
repository intentionally does not commit a root lockfile because it publishes
Skills rather than consuming them.

## Runtime

`shopify-media-sync` selects an executor in this order:

1. an explicit trusted `SHOPIFY_MEDIA_SYNC_BINARY`;
2. a fixed Release asset whose SHA-256 matches `runtime.json`;
3. the bundled Go implementation when Go is available;
4. a structured `NEEDS_SETUP` result.

The current preview has neither a binary Release nor an explicit trusted
binary, so systems without Go fail closed.
Optional Lark/Feishu inputs require `lark-cli`; local CSV, XLSX, JSON, zip, and
directory inputs do not.

`theme-template-sync` uses an explicit trusted `THEME_TEMPLATE_SYNC_BINARY`
when configured; otherwise its POSIX launcher builds the bundled Go module with
the local toolchain. It never downloads an executor. Real remote operations
also require an authenticated Shopify CLI session; repository tests use only
fake/local adapters.

## Safety

- Planning and preview do not write to Shopify.
- Remote execution is exposed only through a reviewed `apply` preview followed
  by identity-matched `apply --execute`.
- Credentials are read only from explicit supported configuration and are
  never printed.
- The repository uses synthetic fixtures and scans for credentials, absolute
  personal paths, real-looking target IDs, and operator-supplied prohibited
  identifiers.

## Development

Requirements:

- Node.js 22.20 or newer (required by the pinned Skills CLI acceptance suite)
- the Go versions declared by both bundled modules under `skills/`

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

# Agent Skills

`agent-skills` is a personal collection of reusable Agent Skills backed by
deterministic tools. Each installable unit lives at `skills/<skill-name>` and
contains its own instructions, references, launcher, implementation, and tests.

Status: **Public preview**

## Available Skills

- `shopify-media-sync` — plans, previews, executes, inspects, and safely
  recovers Shopify Files media synchronization.
- `web-performance-lab` — collects, inspects, and compares repeatable
  Lighthouse measurements for public web pages without assuming a framework or
  commerce platform.

## Install

Install either Skill into a project with the current Skills CLI:

```bash
npx skills add JulCyan/agent-skills --skill shopify-media-sync --copy
npx skills add JulCyan/agent-skills --skill web-performance-lab --copy
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

### `shopify-media-sync`

The installed Skill selects an executor in this order:

1. an explicit trusted `SHOPIFY_MEDIA_SYNC_BINARY`;
2. a fixed Release asset whose SHA-256 matches `runtime.json`;
3. the bundled Go implementation when Go is available;
4. a structured `NEEDS_SETUP` result.

The current preview has neither a binary Release nor an explicit trusted
binary, so systems without Go fail closed.
Optional Lark/Feishu inputs require `lark-cli`; local CSV, XLSX, JSON, zip, and
directory inputs do not.

### `web-performance-lab`

The POSIX wrapper uses Node.js 22.19 or newer. It runs an explicit trusted
`WEBPERF_BINARY` when provided; otherwise it builds the bundled source with Go
1.25.1 or newer in an executor-owned temporary directory. The locked
Lighthouse runtime also requires npm and a supported local Chrome/Chromium.

`engine setup` is the only engine installation command. It requires explicit
authorization because it connects to the npm registry and writes a locked
engine to the user's cache. Formal measurement uses five sequential runs,
reports medians with dispersion, and compares only identical protocol
fingerprints. Raw evidence can contain target details and must remain outside
Git.

## Safety

- Planning and preview do not write to Shopify.
- Remote execution is exposed only through a reviewed `apply` preview followed
  by identity-matched `apply --execute`.
- Credentials are read only from explicit supported configuration and are
  never printed.
- Web performance collection is intended for public HTTP(S) targets, does not
  mutate them, and does not claim field, RUM, or CrUX behavior.
- The repository uses synthetic fixtures and scans for credentials, absolute
  personal paths, real-looking target IDs, and operator-supplied prohibited
  identifiers.

## Development

Requirements:

- Node.js 22.20 or newer (required by the pinned Skills CLI acceptance suite)
- Go 1.25.1 or newer, as declared by both bundled modules

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

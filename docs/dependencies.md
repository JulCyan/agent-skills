# Dependencies

## Required for repository validation

- Node.js 22.20 or newer for full repository validation
- Go 1.25.1 or newer, as declared by all three bundled modules
- `npx skills@1.5.21` for reproducible discovery and copy-install checks
- `@fission-ai/openspec@1.7.0` for strict OpenSpec validation

All three bundled Go modules currently use only the Go standard library.

The `shopify-media-sync` portable launcher supports Node.js 20.19 or newer.
The installed `theme-template-sync` launcher uses POSIX `sh` and either an
explicit trusted binary or the locally installed Go toolchain.
The `web-performance-lab` wrapper and its locked Lighthouse engine require
Node.js 22.19 or newer. The higher repository requirement comes from the
pinned Skills CLI used by the acceptance suite.

## Product runtime dependencies

`lark-cli` is required only for Lark/Feishu Sheet and Drive inputs. Local CSV,
XLSX, JSON, zip, and directory inputs remain available without it.

An authenticated Shopify CLI is required only for real `theme-template-sync`
remote reads or writes. Validation and tests do not require Shopify access.

`web-performance-lab` builds its bundled Go source for each invocation and
therefore requires Go 1.25.1 or newer. Measurement requires a supported local
Chrome/Chromium. Its locked engine requires npm; `engine setup` explicitly
connects to the npm registry and writes to the user's cache, so it must be
authorized before use.

Formal web performance acceptance uses five sequential attempts, compares only
matching protocol fingerprints, and stores evidence outside Git.

## Binary distribution

`skills/shopify-media-sync/runtime.json` is the integrity contract for a future
Release. Until it declares a published fixed version and platform SHA-256
values, the launcher never downloads an asset.

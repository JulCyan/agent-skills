# Dependencies

## Required for repository validation

- Node.js 20.19 or newer
- Go 1.25.1, as declared by the bundled module
- `npx skills@1.5.21` for reproducible discovery and copy-install checks
- `@fission-ai/openspec@1.7.0` for strict OpenSpec validation

The bundled Go module currently uses only the Go standard library.

## Optional runtime dependency

`lark-cli` is required only for Lark/Feishu Sheet and Drive inputs. Local CSV,
XLSX, JSON, zip, and directory inputs remain available without it.

## Binary distribution

`skills/shopify-media-sync/runtime.json` is the integrity contract for a future
Release. Until it declares a published fixed version and platform SHA-256
values, the launcher never downloads an asset.

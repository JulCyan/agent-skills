# Add Shopify Media Sync

## Why

Agents need a reusable Shopify Files synchronization capability whose planning,
write authorization, execution, readback, and recovery behavior are explicit
and machine-verifiable. The repository also needs one installation contract
that works for technical and non-technical consumers through `npx skills`.

## What Changes

- Add the installable `shopify-media-sync` Skill with self-contained
  instructions, references, launcher, Go executor, and synthetic fixtures.
- Add fixed plan/preview/execute and cold-start recovery contracts.
- Add tracked-snapshot discovery, copy-install, installed-copy drift, public
  content, Node, Go, shell, and OpenSpec verification.
- Document provider versus consumer lockfile ownership and runtime selection.

## Scope

- GitHub-source installation through `npx skills@1.5.21`.
- Local CSV, XLSX, JSON, zip, and directory inputs.
- Optional Lark/Feishu Sheet and Drive inputs when `lark-cli` is available.
- Shopify Files plan, preview, explicit execute, inspect, readback, and recovery.

## Non-goals

- Publishing a compiled binary Release or package-registry artifact.
- Selecting an open-source license in this change.
- Performing a real Shopify write or claiming production support.
- Adding additional Skills.

## Cannot Claim

Passing this change proves repository tests and installability at the verified
Git reference. It does not prove a binary Release, consumer rollout, Shopify
production behavior, Publish, or Live behavior.

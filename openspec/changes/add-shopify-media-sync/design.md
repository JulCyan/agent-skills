# Design

## Architecture

The installable unit is `skills/shopify-media-sync`. `SKILL.md` is the agent
contract; references define configuration and result semantics; the POSIX
wrapper calls a Node launcher; the launcher selects a trusted executor; the Go
executor owns deterministic state transitions and Shopify operations.

The launcher resolves an explicit binary first, then a fixed digest-verified
Release, then bundled Go source. It never downloads `latest` or executes an
unknown artifact. When no trusted executor exists it returns `NEEDS_SETUP`.

## Execution contract

Normal operation is `plan -> apply preview -> apply --execute`. Execute binds
the exact plan SHA, selected stores, store configuration identity, rows, and
actions reviewed during preview. Identity drift fails before mutation.

One executor process owns bounded concurrency, attempt history, evidence,
terminal summary, and recovery decisions. Ambiguous mutation outcomes fail
closed as `NEEDS_ATTENTION`; externally owned JSON work stops at
`WAITING_EXTERNAL`.

## Distribution contract

The provider publishes no root `skills-lock.json`. Each consumer receives its
own lockfile from the Skills CLI and owns its installed copy. Verification uses
a tracked Git archive so ignored local helpers cannot enter the install set.

## Public-content contract

Committed policy contains generic credential, path, target-ID, email, lockfile,
and runtime-evidence checks. A maintainer supplies any private prohibited terms
through `AGENT_SKILLS_FORBIDDEN_TOKENS`; those terms are never committed or
printed in findings.

## Validation

`npm test` runs public scanning, Node tests, the complete Go suite, shell syntax,
and strict OpenSpec validation. The sandbox test copy-installs from a tracked
archive and executes `--help` plus JSON `doctor` outside the repository.

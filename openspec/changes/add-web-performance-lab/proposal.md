# Add Web Performance Lab

## Why

AI agents and developers need a reusable way to measure web performance without
mistaking one Lighthouse sample for ground truth. The capability must make the
measurement protocol, engine version, browser environment, repeated samples,
variability, and comparison compatibility explicit.

## What Changes

- Add the installable `web-performance-lab` Skill backed by a deterministic Go
  CLI named `webperf`.
- Use an exact, explicitly installed Lighthouse package as the measurement
  engine instead of reimplementing Lighthouse scoring in Go.
- Add versioned desktop observed, desktop lab, and mobile lab profiles.
- Add sequential multi-run collection, evidence bundles, statistical summaries,
  strict protocol compatibility, and baseline/candidate comparison.
- Add tracked-snapshot discovery, copy-install, cross-cwd, public-content, Go,
  shell, and OpenSpec verification for the new Skill.

## Scope

- Public HTTP and HTTPS URLs that do not require credentials or browser
  interaction.
- Local diagnosis and same-protocol baseline/candidate comparison.
- Five sequential samples by default and at least three successful samples for
  an aggregate conclusion.
- Stable JSON output and caller-selected local evidence directories.
- Explicit engine setup into a user cache; collection never installs or updates
  dependencies implicitly.

## Non-goals

- Reimplementing Lighthouse audits, metrics, simulation, or scoring in Go.
- Treating one sample as performance acceptance.
- CI gating, hosted dashboards, remote uploads, scheduling, or field-data APIs.
- Authenticated sessions, cookies, private headers, scripted page interaction,
  or platform-specific page discovery.
- Publishing a binary Release or package-registry artifact in this change.
- Selecting an open-source license in this change.

## Cannot Claim

Passing this change proves repository tests, deterministic local fixtures, and
the explicitly recorded external acceptance run. It does not prove end-user
field performance, universal agreement with every DevTools mode, CI stability,
cross-machine comparability, a binary Release, or production support.

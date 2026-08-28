# Tasks

## 1. Specification and contracts

- [x] Add the clean-room proposal, requirements, design, and implementation
  tasks for `web-performance-lab`.
- [x] Validate the new OpenSpec change strictly before implementation.
- [x] Define stable JSON, status, profile, artifact, and comparison contracts
  with no externally supplied identifiers.

## 2. Go CLI and engine lifecycle

- [x] Add failing tests for command parsing, doctor, engine status/setup,
  profiles, URL validation, and JSON error envelopes.
- [x] Implement the `webperf` Go CLI and exact Lighthouse engine manifest using
  the minimum dependencies required by the command contract.
- [x] Add failing then passing tests for process cancellation, no implicit
  install, and safe engine resolution.

## 3. Collection and evidence

- [x] Add failing tests for atomic output creation, sequential attempts,
  incomplete runs, URL sanitization, LHR parsing, and artifact permissions.
- [x] Implement collection with fresh browser state, owned process cleanup,
  complete attempt manifests, raw LHR, and summary generation.
- [x] Add deterministic local fixtures and fake-engine integration coverage.

## 4. Statistics and comparison

- [x] Add failing tests for median, MAD, IQR, official-score aggregation,
  protocol mismatch, and materiality classifications.
- [x] Implement inspect and compare without synthesizing a Lighthouse score or
  hiding instability.

## 5. Skill and distribution

- [x] Add concise `SKILL.md`, UI metadata, references, portable launcher, and
  runtime dependency documentation.
- [x] Add discovery, tracked-snapshot copy-install, cross-cwd, shell, Go, and
  public-content tests for the second Skill.
- [x] Update repository-facing documentation and verification commands without
  adding platform-specific positioning.

## 6. Acceptance and review

- [x] Run Go tests, root `npm test`, Skill validation, formatting, vet/race,
  cross-cwd, copied-install, and public scanning.
- [x] Run five observed-desktop and five lab-desktop samples against the
  operator-supplied external URL without tracking the URL or evidence.
- [x] Obtain independent code and contract/security reviews, resolve findings,
  and rerun affected validation.
- [x] Record only verified results in the final summary; leave field data, CI,
  Release, package publication, and production support unclaimed.

## 7. Optional offline HTML reports

- [x] Add failing then passing tests for explicit single/comparison reports,
  deterministic rendering, `PARTIAL` diagnostic labelling, no-replace output,
  private permissions, active-content escaping, and default zero-HTML behavior.
- [x] Implement hash-verified representative diagnostics, one offline
  standalone renderer, repeated distinct-profile input, strict comparison, and
  structured `REPORT_FAILED` handling without changing collection semantics.
- [x] Document the non-interactive opt-in policy: explicit initial request runs
  after verification; otherwise deliver results first and offer at most once.
- [x] Generate single and comparison HTML from retained real evidence, inspect
  visual/security output, complete independent review, and rerun full repository
  acceptance.
- [x] Add explicit built-in `en`/`zh-CN` report localization with stable English
  default, current-report-request language routing, canonical machine fields,
  and no user-authored template or runtime model translation.
- [x] Generate both locales from retained real evidence, verify deterministic
  bytes/offline output and responsive typography, then rerun independent review
  and full repository acceptance.

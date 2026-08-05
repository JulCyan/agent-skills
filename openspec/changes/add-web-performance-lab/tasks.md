# Tasks

## 1. Specification and contracts

- [x] Add the clean-room proposal, requirements, design, and implementation
  tasks for `web-performance-lab`.
- [x] Validate the new OpenSpec change strictly before implementation.
- [x] Define stable JSON, status, profile, artifact, and comparison contracts
  with no externally supplied identifiers.

## 2. Go CLI and engine lifecycle

- [ ] Add failing tests for command parsing, doctor, engine status/setup,
  profiles, URL validation, and JSON error envelopes.
- [ ] Implement the `webperf` Go CLI and exact Lighthouse engine manifest using
  the minimum dependencies required by the command contract.
- [ ] Add failing then passing tests for process cancellation, no implicit
  install, and safe engine resolution.

## 3. Collection and evidence

- [ ] Add failing tests for atomic output creation, sequential attempts,
  incomplete runs, URL sanitization, LHR parsing, and artifact permissions.
- [ ] Implement collection with fresh browser state, owned process cleanup,
  complete attempt manifests, raw LHR, and summary generation.
- [ ] Add deterministic local fixtures and fake-engine integration coverage.

## 4. Statistics and comparison

- [ ] Add failing tests for median, MAD, IQR, official-score aggregation,
  protocol mismatch, materiality, and inconclusive classifications.
- [ ] Implement inspect and compare without synthesizing a Lighthouse score or
  hiding instability.

## 5. Skill and distribution

- [ ] Add concise `SKILL.md`, UI metadata, references, portable launcher, and
  runtime dependency documentation.
- [ ] Add discovery, tracked-snapshot copy-install, cross-cwd, shell, Go, and
  public-content tests for the second Skill.
- [ ] Update repository-facing documentation and verification commands without
  adding platform-specific positioning.

## 6. Acceptance and review

- [ ] Run Go tests, root `npm test`, Skill validation, formatting, vet/race,
  cross-cwd, copied-install, and public scanning.
- [ ] Run five observed-desktop and five lab-desktop samples against the
  operator-supplied external URL without tracking the URL or evidence.
- [ ] Obtain independent code and contract/security reviews, resolve findings,
  and rerun affected validation.
- [ ] Record only verified results in the final summary; leave field data, CI,
  Release, package publication, and production support unclaimed.

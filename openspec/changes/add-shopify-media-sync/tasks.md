# Tasks

## 1. Repository contract

- [x] Add neutral repository, contribution, security, publication, dependency,
  maintenance, and verification documentation.
- [x] Add OpenSpec configuration and this change without environment-specific
  paths or identifiers.

## 2. Product Skill

- [x] Add `shopify-media-sync` instructions, references, launcher, runtime
  manifest, deterministic Go executor, and synthetic fixtures.
- [x] Keep planning and preview mutation-free; require explicit execution
  authorization and stable identity binding.
- [x] Preserve terminal status, readback, attempt history, and safe recovery
  behavior.

## 3. Distribution and governance

- [x] Verify `npx skills@1.5.21 --list` exposes exactly one Skill.
- [x] Verify a tracked snapshot copy-installs exactly one self-contained Skill.
- [x] Verify consumer lockfile hashes report `MATCH`, `DRIFT`, and
  `NEEDS_SETUP` correctly.
- [x] Keep private prohibited terms outside committed policy and accept them at
  runtime through `AGENT_SKILLS_FORBIDDEN_TOKENS`.

## 4. Acceptance

- [x] Run `npm test` with the private prohibited-term environment configured.
- [x] Run the skill-creator validator for `skills/shopify-media-sync`.
- [x] Audit tracked files, commit metadata, branch name, and PR text for public
  identifiers and credentials.
- [x] Verify the GitHub branch SHA before and after branch installation.
- [x] Record only evidence established by this change; leave Release, consumer
  rollout, Shopify production, Publish, and Live unclaimed.

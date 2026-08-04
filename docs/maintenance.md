# Maintenance

`agent-skills` is the canonical repository for the Skills it publishes.
Changes enter through OpenSpec, tests, review, and a versioned Git reference.

An installed Skill is a consumer-owned copy. The consumer's `skills-lock.json`
records its origin, ref, path, and computed folder hash. A provider repository
does not need its own root lockfile unless it also consumes another Skill.

To check an installed copy from a clone of this repository:

```bash
node scripts/verify-installed-skill.mjs \
  --project <consumer-project> \
  --skill shopify-media-sync
```

The verifier returns `MATCH`, `DRIFT`, or `NEEDS_SETUP`. Fixes are made here,
validated from a fresh tracked snapshot, and then installed into consumers at
an explicit reviewed ref. Installed copies are not edited as a substitute for
provider changes.

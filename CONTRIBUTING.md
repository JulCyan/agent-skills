# Contributing

This repository uses an OpenSpec change under `openspec/changes/` for behavior
or architecture updates. Product Skills belong under `skills/<skill-name>`.

Before submitting a change:

```bash
npm test
python3 <skill-creator-path>/scripts/quick_validate.py skills/<skill-name>
```

When adding a Skill, also:

- add exactly one `skills/<skill-name>/SKILL.md` product entry;
- keep its launcher, runtime contract, references, source, and fixtures inside
  that Skill directory;
- add discovery and tracked-snapshot copy-install coverage;
- run the installed copy from a caller cwd outside the provider repository;
- document optional dependencies, mutation authority, recovery, and lockfile
  ownership;
- use only synthetic fixtures and run the public scanner with the maintainer's
  private prohibited-term environment configured.

Use synthetic fixtures. Do not include credentials, environment files, real
store data, customer data, personal absolute paths, operational artifacts, or
organization-specific identifiers.

The public preview does not yet accept code contributions that require a
license grant. Issues and design discussion remain welcome. Commit messages use
`type(scope): 中文描述`.

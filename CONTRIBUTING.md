# Contributing

This repository uses an OpenSpec change under `openspec/changes/` for behavior
or architecture updates. Product Skills belong under `skills/<skill-name>`.

Before submitting a change:

```bash
npm test
python3 <skill-creator-path>/scripts/quick_validate.py skills/<skill-name>
```

Use synthetic fixtures. Do not include credentials, environment files, real
store data, customer data, personal absolute paths, operational artifacts, or
organization-specific identifiers.

The public preview does not yet accept code contributions that require a
license grant. Issues and design discussion remain welcome. Commit messages use
`type(scope): 中文描述`.

# Verification

The acceptance suite checks these boundaries:

- the public-content scanner checks the working tree, exact Git index blobs,
  tracked paths, and Git metadata and returns `PASS`;
- Node launcher and installation-governance tests pass;
- the complete Go test suite passes;
- the POSIX wrapper passes shell syntax validation;
- every OpenSpec change passes strict validation;
- `npx skills@1.5.21 --list` finds exactly `shopify-media-sync` and
  `theme-template-sync`;
- a tracked Git archive copy-installs exactly two Skills into a disposable
  consumer and verifies both lock hashes;
- both installed wrappers run `--help` outside the repository; the media Skill
  additionally runs synthetic JSON `doctor`, `plan`, and `inspect` checks.

Run the full acceptance entrypoint:

```bash
npm test
```

These checks do not perform a real Shopify mutation, publish a Release, or
prove production behavior.

User-facing installation examples intentionally use unpinned `npx skills` so
consumers receive supported fixes. Acceptance tests pin `skills@1.5.21` so a
test result is attributable to one exact CLI version.

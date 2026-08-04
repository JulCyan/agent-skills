# Verification

The acceptance suite checks these boundaries:

- the public-content scanner checks the working tree, exact Git index blobs,
  tracked paths, and Git metadata and returns `PASS`;
- Node launcher and installation-governance tests pass;
- the complete Go test suite passes;
- the POSIX wrapper passes shell syntax validation;
- every OpenSpec change passes strict validation;
- `npx skills@1.5.21 --list` finds exactly `shopify-media-sync`;
- a tracked Git archive copy-installs exactly one Skill into a disposable
  consumer;
- the installed wrapper runs `--help`, JSON `doctor`, `plan`, and `inspect`
  outside the repository.

Run the full acceptance entrypoint:

```bash
npm test
```

These checks do not perform a real Shopify mutation, publish a Release, or
prove production behavior.

User-facing installation examples intentionally use unpinned `npx skills` so
consumers receive supported fixes. Acceptance tests pin `skills@1.5.21` so a
test result is attributable to one exact CLI version.

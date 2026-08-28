# Verification

The acceptance suite checks these boundaries:

- the public-content scanner checks the working tree, exact Git index blobs,
  tracked paths, and Git metadata and returns `PASS`;
- Node launcher and installation-governance tests pass;
- both complete Go module test suites pass;
- both POSIX wrappers pass shell syntax validation;
- every OpenSpec change passes strict validation;
- `npx skills@1.5.21 --list` finds exactly `shopify-media-sync` and
  `web-performance-lab`;
- a tracked Git archive copy-installs exactly two Skills into a disposable
  consumer;
- the installed media wrapper runs `--help`, JSON `doctor`, `plan`, and
  `inspect` outside the repository;
- the installed web performance wrapper runs `--help`, JSON `doctor`, and
  `profiles list` from a caller cwd outside the provider without invoking
  `engine setup` or creating a measurement bundle.

Run the full acceptance entrypoint:

```bash
npm test
```

These checks do not perform a real remote mutation, measure a real target URL,
publish a Release, prove CI execution, or establish field/RUM/CrUX or
production behavior. Web performance acceptance of a real target separately
requires five-run evidence under an explicit protocol; that evidence stays
outside Git.

User-facing installation examples intentionally use unpinned `npx skills` so
consumers receive supported fixes. Acceptance tests pin `skills@1.5.21` so a
test result is attributable to one exact CLI version.

# Verification

The acceptance suite checks these boundaries:

- the public-content scanner returns `PASS`;
- Node launcher and installation-governance tests pass;
- the complete Go test suite passes;
- the POSIX wrapper passes shell syntax validation;
- every OpenSpec change passes strict validation;
- `npx skills@1.5.21 --list` finds exactly `shopify-media-sync`;
- a tracked Git archive copy-installs exactly one Skill into a disposable
  consumer;
- the installed wrapper runs `--help` and JSON `doctor` outside the repository.

Run the full acceptance entrypoint:

```bash
npm test
```

These checks do not perform a real Shopify mutation, publish a Release, or
prove production behavior.

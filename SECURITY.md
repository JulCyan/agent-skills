# Security Policy

## Supported versions

The repository is currently a public preview without a supported binary
release. Security fixes target the latest default-branch source.

## Reporting

Do not include credentials, environment files, store domains, resource IDs,
run artifacts, or customer data in a public issue, pull request, screenshot, or
log. Use synthetic reproduction data and GitHub's private vulnerability
reporting channel when available.

Run `npm run scan:public` before sharing a patch. Maintainers can set
`AGENT_SKILLS_FORBIDDEN_TOKENS` to a comma- or newline-separated private list;
the values are checked at runtime and are not committed to the repository.

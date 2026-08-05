# Design

## Product boundary

`web-performance-lab` is a domain-neutral Skill for public web URLs. Its normal
job is to diagnose one URL or compare two evidence bundles produced under the
same measurement protocol. The Skill teaches an agent how to call `webperf`;
all measurement, statistics, redaction, compatibility, and process lifecycle
behavior lives in the deterministic executor.

The first release intentionally has no platform detection, account model,
authentication, remote mutation, background service, upload, or CI gate.

## Architecture

The installable unit is `skills/web-performance-lab`. A portable launcher finds
an explicitly trusted binary or runs the bundled Go source. The Go CLI owns the
command contract, profile registry, setup checks, sequential orchestration,
artifact store, statistics, comparison, and structured errors.

Lighthouse remains an external measurement engine. `engine setup` installs one
exact package version into a versioned user cache. Collection resolves only the
locked engine, invokes its official CLI through Node, and records the resolved
Node, Lighthouse, and Chrome versions. It never invokes an unversioned `npx`,
downloads `latest`, or updates the engine during collection.

On Unix, the cache hierarchy rejects symlinks, unsafe entry types,
group/other-writable paths, and entries not owned by the current effective user.
Setup disables npm bin links and records a deterministic SHA-256 over every
payload path, entry type, permission mode, and file byte. Runtime verifies the
marker and takes descriptor-bound tree snapshots before and after resolving host
tools. Any identity or digest drift fails closed without executing or replacing
the cache. Non-Unix platforms still enforce type, no-symlink, descriptor
identity, and the full digest, but do not claim portable ACL ownership or mode
verification. Continuously malicious same-user processes after Runtime returns
remain outside this local cache threat boundary.

An integrity-schema change selects a new internal cache generation. A legacy
generation remains untouched and cannot block setup of the new generation.

The data flow is:

`Skill -> webperf -> profile -> Lighthouse -> Chrome -> LHR -> summary`

## Command contract

The first command families are:

- `webperf --json doctor`
- `webperf --json profiles list`
- `webperf --json engine status`
- `webperf --json engine setup`
- `webperf --json collect --url <url> --profile <name> --runs 5 --out <dir>`
- `webperf --json inspect --run <dir>`
- `webperf --json compare --baseline <dir> --candidate <dir>`
- `webperf raw lighthouse -- <official flags>`

Human output is concise by default. With `--json`, stdout contains exactly one
documented JSON envelope and progress goes to stderr. `doctor` and `engine
status` are read-only. `engine setup` is the only command that installs the
engine. High-level collection requires an explicit profile and output path.

The raw command is an expert escape hatch. It uses the locked engine but does
not claim the high-level protocol, aggregation, redaction, or comparison
guarantees.

## Measurement profiles

Profiles are compiled, versioned contracts rather than editable ambient files:

- `desktop-observed-v1` uses a desktop viewport and provided throttling to
  report values observed by the tested Chrome process.
- `desktop-lab-v1` uses a desktop viewport and Lighthouse simulation.
- `mobile-lab-v1` uses a mobile viewport and Lighthouse simulation.

Each profile resolves to explicit Lighthouse flags. A protocol fingerprint is
computed from the schema version, profile definition, Lighthouse version,
Chrome version, Node version, operating system, architecture, and relevant
runtime flags. Comparison is fail-closed when fingerprints differ. A future
explicit override may relax browser patch differences, but v1 remains strict.

## Collection lifecycle

Collection validates the URL and environment, creates a caller-selected bundle
directory atomically, and runs samples one at a time. Each sample uses a fresh
Chrome user-data directory. Collection does not warm the page, run concurrent
samples, or silently retry failures.

Every attempt is represented in the manifest. A failed attempt remains failed;
the user may start a new bundle instead of biasing the sample by selective
retry. At least three successful samples are required for aggregate metrics.
The default and recommended count is five.

On Unix, signals terminate the owned Lighthouse/Chrome process group with a
bounded TERM-to-KILL sequence. Other platform builds terminate only the direct
Lighthouse process; descendant cleanup is not guaranteed. The executor
finalizes the manifest as interrupted when possible and removes only temporary
browser data it can prove it owns. The caller-selected bundle remains available
for diagnosis.

## Evidence and statistics

One run bundle contains:

- `manifest.json` with command status, timestamps, attempts, sanitized requested
  URL, final URL, and artifact references;
- `protocol.json` with resolved profile and runtime fingerprint;
- `samples/run-N.lhr.json` with each official Lighthouse result;
- `summary.json` with per-sample metrics, median, minimum, maximum, median
  absolute deviation, interquartile range, warnings, and comparison readiness.

The CLI never fabricates a Lighthouse score from median metrics. It reports the
distribution and median of the official per-sample performance scores. Timing
metrics and CLS are aggregated independently.

LCP identity is extracted when the locked LHR schema provides it. Identity
changes, final-URL changes, failed samples, and high relative dispersion produce
explicit instability warnings instead of being hidden behind the median.

## Comparison model

Comparison first verifies schema and protocol fingerprints. Compatible bundles
are classified per metric as `improvement`, `no_material_change`, `regression`,
or `inconclusive`. Initial materiality floors are:

- timing metrics: the greater of ten percent and twice the observed group MAD;
- performance score: five absolute points;
- CLS: 0.02 absolute.

The default comparison command returns a successful analysis even when it finds
a regression; the result status carries the classification. CI-specific exit
behavior is outside v1. Invalid bundles, incompatible protocols, and incomplete
analysis return nonzero structured errors.

## Privacy and public-content boundary

High-level commands accept only HTTP and HTTPS URLs without userinfo. Persisted
summary URLs omit query values and fragments. Raw LHR files are local evidence,
created with restricted permissions, and are never repository fixtures.

The repository contains only synthetic `example.test` fixtures and deterministic
local test pages. It contains no externally supplied URL, runtime report,
credential, cookie, account identifier, organization-specific vocabulary, or
personal absolute path.

## Error model

Machine-readable statuses include `OK`, `PARTIAL`, `INCONCLUSIVE`,
`NEEDS_SETUP`, `INVALID_INPUT`, `ENGINE_FAILED`, `NAVIGATION_FAILED`,
`PARSE_FAILED`, `INCOMPATIBLE_PROTOCOL`, and `INTERRUPTED`. JSON errors contain a
stable code, safe message, and remediation hint without credentials or raw
query values.

Fewer than three successful samples leaves evidence but returns an incomplete
analysis. The bundle root is claimed with a sibling claim token and no-replace
directory creation; a pending manifest records initialization and
`manifest.json` is the final commit point. Existing output paths are never
overwritten.

## Distribution

The Skill is self-contained after copy installation and resolves its launcher,
references, Go module, and synthetic fixtures relative to its own directory.
It preserves the caller working directory and accepts all user paths relative
to that caller.

The source-backed preview requires Go, Node, Chrome, and an explicitly installed
locked Lighthouse engine. A future fixed-version, digest-verified binary Release
can remove the Go build requirement without changing the command contract.

## Validation

Unit tests cover command parsing, JSON envelopes, URL sanitization, profile
fingerprints, LHR parsing, median/MAD/IQR, comparison materiality, atomic output,
and structured failures. Integration tests use a deterministic local HTTP page,
a fake engine for failure/process cases, and the real locked Lighthouse engine
when the required browser runtime is available.

Repository acceptance covers Skill validation, public scanning, discovery,
tracked-snapshot copy installation, execution from an unrelated working
directory, signal cleanup, and full Go tests. External acceptance uses an
operator-supplied URL for five sequential observed-desktop and five sequential
lab-desktop samples. External URLs and evidence remain untracked.

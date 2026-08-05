# Web Performance Lab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development or superpowers:executing-plans to
> implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Build a domain-neutral `webperf` Go CLI and companion Skill that use a
locked official Lighthouse engine for repeated, evidence-backed local web
performance experiments.

**Architecture:** A portable Skill launcher builds or selects the Go executor
without changing the caller working directory. The Go executor owns profiles,
engine setup, sequential collection, evidence, statistics, comparison, and JSON
contracts; Node, Lighthouse 13.4.1, and Chrome remain explicit measurement
dependencies.

**Tech Stack:** Go 1.25.1 standard library, Node.js 22.20+, Lighthouse 13.4.1,
Google Chrome/Chromium, POSIX shell, Node test runner, OpenSpec 1.7.0.

## Global Constraints

- Require an explicit versioned profile and output directory for collection.
- Run five samples by default and reject fewer than three requested samples.
- Run samples sequentially with no silent retry.
- Never install or update Lighthouse during collection.
- Never synthesize a Lighthouse score from median metrics.
- Reject incompatible protocol fingerprints before comparison.
- Keep externally supplied URLs and evidence outside tracked files.
- Keep `--json` stdout to one stable JSON document; diagnostics use stderr.
- Use only synthetic `example.test` and local fixtures in the repository.
- Do not publish a Release, package, field-data claim, or CI performance gate.

## File map

- `skills/web-performance-lab/SKILL.md`: short agent workflow and safety rules.
- `skills/web-performance-lab/agents/openai.yaml`: discovery metadata.
- `skills/web-performance-lab/references/cli-contract.md`: commands, JSON, and
  status semantics.
- `skills/web-performance-lab/references/measurement-protocol.md`: profiles,
  aggregation, comparison, and evidence interpretation.
- `skills/web-performance-lab/scripts/webperf`: portable POSIX entrypoint.
- `skills/web-performance-lab/scripts/launcher.mjs`: explicit binary or bundled
  source resolution while preserving caller cwd.
- `skills/web-performance-lab/scripts/webperf-go/cmd/webperf/main.go`: signal
  context and process exit.
- `skills/web-performance-lab/scripts/webperf-go/internal/app`: command parsing,
  dependency assembly, human/JSON rendering.
- `skills/web-performance-lab/scripts/webperf-go/internal/contract`: stable
  envelopes, statuses, errors, and exit codes.
- `skills/web-performance-lab/scripts/webperf-go/internal/profile`: versioned
  Lighthouse profile definitions and protocol fingerprints.
- `skills/web-performance-lab/scripts/webperf-go/internal/engine`: locked npm
  assets, setup/status, Chrome discovery, Lighthouse execution, process groups.
- `skills/web-performance-lab/scripts/webperf-go/internal/safeurl`: validation
  and display redaction.
- `skills/web-performance-lab/scripts/webperf-go/internal/lhr`: locked LHR
  parsing into stable metric samples.
- `skills/web-performance-lab/scripts/webperf-go/internal/stats`: median, MAD,
  quartiles, and metric summaries.
- `skills/web-performance-lab/scripts/webperf-go/internal/bundle`: restricted,
  non-overwriting evidence storage and readers.
- `skills/web-performance-lab/scripts/webperf-go/internal/collect`: sequential
  attempts, manifest transitions, and summary creation.
- `skills/web-performance-lab/scripts/webperf-go/internal/compare`: strict
  compatibility and materiality classifications.
- `scripts/tests/webperf-launcher.test.mjs`: copied launcher and cwd behavior.
- `scripts/tests/webperf-install.test.mjs`: tracked-snapshot installation and
  invocation outside the provider repository.

---

### Task 1: CLI contracts and profiles

**Files:**
- Create: `skills/web-performance-lab/scripts/webperf-go/go.mod`
- Create: `skills/web-performance-lab/scripts/webperf-go/cmd/webperf/main.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/contract/model.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/app/app.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/app/app_test.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/profile/profile.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/profile/profile_test.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/safeurl/url.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/safeurl/url_test.go`

**Interfaces:**
- Produces: `app.Run(context.Context, []string, app.Dependencies) int`.
- Produces: `profile.Resolve(name string) (profile.Profile, error)` and
  `profile.List() []profile.Profile`.
- Produces: `safeurl.Validate(string) (*url.URL, error)` and
  `safeurl.Display(*url.URL) string`.
- Produces: `contract.Envelope`, `contract.CommandError`, and stable statuses.

- [ ] **Step 1: Write failing parser, profile, and URL tests**

```go
func TestCollectRequiresExplicitProfile(t *testing.T) {
    code, report := runJSON(t, "--json", "collect", "--url",
        "https://example.test", "--out", "run")
    if code != contract.ExitInvalidInput || report.Status != contract.InvalidInput {
        t.Fatalf("code=%d status=%s", code, report.Status)
    }
}

func TestDisplayRedactsQueryValues(t *testing.T) {
    parsed, err := Validate("https://example.test/p?a=secret&b=two#fragment")
    if err != nil { t.Fatal(err) }
    if got := Display(parsed); got != "https://example.test/p?a=REDACTED&b=REDACTED" {
        t.Fatalf("display=%q", got)
    }
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test ./internal/app ./internal/profile ./internal/safeurl`

Expected: FAIL because packages and functions do not exist.

- [ ] **Step 3: Implement the minimum stable command and profile contracts**

```go
type Profile struct {
    Name             string   `json:"name"`
    FormFactor       string   `json:"formFactor"`
    ThrottlingMethod string   `json:"throttlingMethod"`
    LighthouseArgs   []string `json:"lighthouseArgs"`
}

type Envelope struct {
    SchemaVersion int           `json:"schemaVersion"`
    Command       string        `json:"command"`
    Status        Status        `json:"status"`
    Data          any           `json:"data,omitempty"`
    Warnings      []string      `json:"warnings,omitempty"`
    Error         *CommandError `json:"error,omitempty"`
}
```

- [ ] **Step 4: Run tests, formatting, and help smoke test**

Run: `cd skills/web-performance-lab/scripts/webperf-go && gofmt -w . && go test ./internal/app ./internal/profile ./internal/safeurl && go run ./cmd/webperf --help`

Expected: PASS and help lists doctor, profiles, engine, collect, inspect,
compare, and raw.

### Task 2: Locked engine lifecycle

**Files:**
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/npm/package.json`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/npm/package-lock.json`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/assets.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/engine.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/engine_test.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/process_unix.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/engine/process_other.go`

**Interfaces:**
- Produces: `engine.Manager.Status(context.Context) engine.Status`.
- Produces: `engine.Manager.Setup(context.Context) (engine.Status, error)`.
- Produces: `engine.Manager.Runtime(context.Context) (engine.Runtime, error)`.
- Produces: `engine.Runner.Run(context.Context, engine.Request) engine.Result`.
- Consumes: `profile.Profile.LighthouseArgs`.

- [ ] **Step 1: Add the locked npm manifest and failing lifecycle tests**

```json
{
  "name": "webperf-lighthouse-engine",
  "private": true,
  "dependencies": { "lighthouse": "13.4.1" }
}
```

```go
func TestRuntimeNeverUsesGlobalLighthouse(t *testing.T) {
    manager := testManager(t, emptyEngineRoot(t))
    _, err := manager.Runtime(context.Background())
    if !errors.Is(err, ErrNeedsSetup) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Generate the lockfile and verify RED**

Run: `cd skills/web-performance-lab/scripts/webperf-go/internal/engine/npm && npm install --package-lock-only --ignore-scripts --no-audit --no-fund`

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test ./internal/engine`

Expected: FAIL because Manager is not implemented.

- [ ] **Step 3: Implement explicit `npm ci` setup and runtime verification**

Use `go:embed npm/package.json npm/package-lock.json`, `os.UserCacheDir()`, a
unique sibling staging directory, `npm ci --ignore-scripts --no-audit
--no-fund`, and an atomic rename into
`webperf/engines/lighthouse/13.4.1`. Validate the installed package version and
CLI path before returning `Runtime`.

- [ ] **Step 4: Implement owned process-group cancellation**

On Unix set `SysProcAttr.Setpgid = true`; on cancellation send SIGTERM to the
negative pid, wait a bounded interval, then SIGKILL only that process group.
Other platforms terminate the direct process. Never target an unresolved pid.

- [ ] **Step 5: Run lifecycle tests**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test -race ./internal/engine`

Expected: PASS with missing engines reported as `NEEDS_SETUP` and no global
fallback.

### Task 3: LHR parsing, statistics, and evidence bundles

**Files:**
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/lhr/lhr.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/lhr/lhr_test.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/lhr/testdata/sample.json`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/stats/stats.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/stats/stats_test.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/bundle/model.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/bundle/store.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/bundle/store_test.go`

**Interfaces:**
- Produces: `lhr.Parse([]byte) (lhr.Sample, error)`.
- Produces: `stats.Summarize([]float64) stats.Distribution`.
- Produces: `bundle.Create(path string, manifest Manifest, protocol Protocol)
  (*bundle.Store, error)` and `bundle.Open(path string) (*bundle.Store, error)`.

- [ ] **Step 1: Write failing fixture, statistics, and no-overwrite tests**

```go
func TestSummarizeUsesMedianMADAndIQR(t *testing.T) {
    got := Summarize([]float64{1, 2, 3, 4, 100})
    if got.Median != 3 || got.MAD != 1 || got.Min != 1 || got.Max != 100 {
        t.Fatalf("distribution=%+v", got)
    }
}

func TestCreateDoesNotOverwrite(t *testing.T) {
    target := filepath.Join(t.TempDir(), "run")
    if _, err := Create(target, Manifest{}, Protocol{}); err != nil { t.Fatal(err) }
    if _, err := Create(target, Manifest{}, Protocol{}); !errors.Is(err, ErrExists) {
        t.Fatalf("err=%v", err)
    }
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test ./internal/lhr ./internal/stats ./internal/bundle`

Expected: FAIL because parsers and stores do not exist.

- [ ] **Step 3: Implement stable parsing and restricted evidence writes**

Parse only the locked fields: `lighthouseVersion`, `finalDisplayedUrl`,
`categories.performance.score`, FCP, LCP, Speed Index, TBT, CLS,
`environment.benchmarkIndex`, warnings, and optional LCP selector. Write
directories as `0700`, JSON files as `0600`, and artifact references relative
to the bundle.

- [ ] **Step 4: Run focused tests and fixture parse**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test ./internal/lhr ./internal/stats ./internal/bundle`

Expected: PASS and a malformed fixture returns `PARSE_FAILED` without panic.

### Task 4: Sequential collection and summaries

**Files:**
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/collect/collect.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/collect/collect_test.go`
- Modify: `skills/web-performance-lab/scripts/webperf-go/internal/app/app.go`

**Interfaces:**
- Consumes: `engine.Runner`, `bundle.Store`, `lhr.Parse`, `stats.Summarize`.
- Produces: `collect.Service.Run(context.Context, collect.Request)
  (bundle.Summary, error)`.

- [ ] **Step 1: Write failing sequential and incomplete-run tests**

```go
func TestRunIsSequentialAndDoesNotRetry(t *testing.T) {
    runner := &recordingRunner{results: fiveSuccessfulResults(t)}
    _, err := serviceWith(runner).Run(context.Background(), validRequest(t, 5))
    if err != nil { t.Fatal(err) }
    if runner.maxConcurrent != 1 || runner.calls != 5 {
        t.Fatalf("max=%d calls=%d", runner.maxConcurrent, runner.calls)
    }
}

func TestRunRequiresThreeSuccessfulSamples(t *testing.T) {
    runner := &recordingRunner{results: twoSuccessThreeFailure(t)}
    _, err := serviceWith(runner).Run(context.Background(), validRequest(t, 5))
    if !errors.Is(err, ErrIncomplete) || runner.calls != 5 { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Run collection tests and verify RED**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test ./internal/collect`

Expected: FAIL because Service does not exist.

- [ ] **Step 3: Implement five-attempt collection and summary generation**

Validate `runs >= 3`, create the bundle before navigation, call the runner in a
plain loop, record every attempt, parse successful LHRs, and aggregate each
metric independently. Preserve official score samples and add warnings for LCP
identity or final-URL drift.

- [ ] **Step 4: Run collection and app tests**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test -race ./internal/collect ./internal/app`

Expected: PASS with one active engine call at most and no silent retries.

### Task 5: Inspect, compare, and raw escape hatch

**Files:**
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/compare/compare.go`
- Create: `skills/web-performance-lab/scripts/webperf-go/internal/compare/compare_test.go`
- Modify: `skills/web-performance-lab/scripts/webperf-go/internal/app/app.go`
- Modify: `skills/web-performance-lab/scripts/webperf-go/internal/app/app_test.go`

**Interfaces:**
- Produces: `compare.Bundles(baseline, candidate bundle.Summary,
  baselineProtocol, candidateProtocol bundle.Protocol) (compare.Report, error)`.
- Consumes: `engine.Manager.Runtime` for `raw lighthouse`.

- [ ] **Step 1: Write failing compatibility and materiality tests**

```go
func TestCompareRejectsFingerprintMismatch(t *testing.T) {
    _, err := Bundles(summary(1000), summary(900), protocol("a"), protocol("b"))
    if !errors.Is(err, ErrIncompatibleProtocol) { t.Fatalf("err=%v", err) }
}

func TestTimingUsesGreaterOfTenPercentAndTwiceMAD(t *testing.T) {
    got := classifyLowerBetter(1000, 880, 20, 30, timingPolicy)
    if got.Classification != Improvement || got.MaterialityFloor != 100 {
        t.Fatalf("result=%+v", got)
    }
}
```

- [ ] **Step 2: Run compare tests and verify RED**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test ./internal/compare ./internal/app`

Expected: FAIL because compare commands do not exist.

- [ ] **Step 3: Implement strict comparison, inspect, and raw forwarding**

Time metrics are lower-better, score is higher-better, and CLS is lower-better.
Use `max(10% baseline, 2*max(MADs))`, `max(5, 2*max(score MADs))`, and
`max(0.02, 2*max(CLS MADs))`. `raw lighthouse` forwards arguments only to the
locked CLI and labels its output outside the high-level protocol guarantees.

- [ ] **Step 4: Run all Go tests**

Run: `cd skills/web-performance-lab/scripts/webperf-go && go test -race ./...`

Expected: PASS.

### Task 6: Portable Skill and launcher

**Files:**
- Create: `skills/web-performance-lab/SKILL.md`
- Create: `skills/web-performance-lab/agents/openai.yaml`
- Create: `skills/web-performance-lab/references/cli-contract.md`
- Create: `skills/web-performance-lab/references/measurement-protocol.md`
- Create: `skills/web-performance-lab/scripts/webperf`
- Create: `skills/web-performance-lab/scripts/launcher.mjs`
- Create: `scripts/tests/webperf-launcher.test.mjs`

**Interfaces:**
- Consumes: `WEBPERF_BINARY` as an explicit trusted binary override.
- Produces: executable `scripts/webperf` that preserves caller cwd and args.

- [ ] **Step 1: Write failing launcher tests**

```js
test('webperf launcher preserves caller cwd and arguments', async () => {
  const result = await runWithFakeBinary(['profiles', 'list']);
  assert.equal(await realpath(result.cwd), await realpath(caller));
  assert.deepEqual(result.args, ['profiles', 'list']);
});
```

- [ ] **Step 2: Run launcher tests and verify RED**

Run: `node --test scripts/tests/webperf-launcher.test.mjs`

Expected: FAIL because the launcher does not exist.

- [ ] **Step 3: Implement the launcher and concise Skill workflow**

Resolve `WEBPERF_BINARY` first. Otherwise build the bundled module into a unique
executor-owned temporary directory and run it with the original cwd. Return a
structured `NEEDS_SETUP` response when Go is absent. The Skill sequence is
doctor, profiles list, explicit engine setup only with write authorization,
collect, inspect, compare; it forbids single-run acceptance and tracked runtime
evidence.

- [ ] **Step 4: Validate Skill metadata and launcher behavior**

Run: `python3 <skill-creator-path>/scripts/quick_validate.py skills/web-performance-lab`

Run: `node --test scripts/tests/webperf-launcher.test.mjs`

Expected: PASS.

### Task 7: Repository discovery and copied installation

**Files:**
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`
- Modify: `docs/dependencies.md`
- Modify: `docs/verification.md`
- Modify: `package.json`
- Modify: `.github/workflows/ci.yml`
- Modify: `scripts/tests/skills-discovery.test.mjs`
- Modify: `scripts/tests/sandbox-install.test.mjs`
- Create: `scripts/tests/webperf-install.test.mjs`

**Interfaces:**
- Produces: two discoverable product Skills and complete root validation.

- [ ] **Step 1: Update discovery expectations first and verify RED**

Expect `Found 2 skills`, `shopify-media-sync`, and
`web-performance-lab`; copied installation must report `Installed 2 skills`.

Run: `node --test scripts/tests/skills-discovery.test.mjs scripts/tests/sandbox-install.test.mjs scripts/tests/webperf-install.test.mjs`

Expected: FAIL until the Skill and repository scripts are wired.

- [ ] **Step 2: Wire Go, shell, copy-install, and CI validation**

Add separate npm scripts for both Go modules and both shell launchers. Configure
setup-go cache paths for both `go.mod` files. The webperf install test copies a
tracked snapshot, runs help, JSON doctor, and profiles list from a separate cwd,
and verifies no bundle appears in the caller.

- [ ] **Step 3: Run repository acceptance**

Run: `npm test`

Expected: public scan PASS, all Node/Go/shell tests PASS, and both OpenSpec
changes validate strictly.

### Task 8: Real-engine acceptance, review, and closure

**Files:**
- Modify: `openspec/changes/add-web-performance-lab/tasks.md`
- Modify only if findings require it: files from Tasks 1-7.

**Interfaces:**
- Consumes: operator-supplied external URL without persisting it.
- Produces: untracked evidence and a verified final summary.

- [ ] **Step 1: Run static and local acceptance**

Run: `cd skills/web-performance-lab/scripts/webperf-go && gofmt -w . && go vet ./... && go test -race ./...`

Run: `sh -n skills/web-performance-lab/scripts/webperf`

Run: `npm test`

- [ ] **Step 2: Set up the locked engine explicitly in an untracked cache**

Run the installed launcher with `engine setup`, then `doctor`; verify reported
Node, Lighthouse, and Chrome versions and confirm the command does not write to
the repository or caller cwd.

- [ ] **Step 3: Execute external acceptance**

Run five `desktop-observed-v1` samples and five `desktop-lab-v1` samples into a
temporary directory. Verify raw LHR values match parsed samples, inspect both
summaries, and compare only compatible bundles. Do not stage the URL or evidence.

- [ ] **Step 4: Obtain independent reviews and resolve findings**

Dispatch one reviewer for Go/process/security correctness and one reviewer for
CLI/Skill/spec/test coverage. Apply valid findings, rerun focused tests, then
rerun complete acceptance.

- [ ] **Step 5: Mark only completed OpenSpec tasks and commit implementation**

Use commit message:

```text
feat(perf): 新增通用网页性能实验室 Skill
```

- [ ] **Step 6: Verify final repository and evidence state**

Run: `git status --short`, `git diff origin/main...HEAD --check`, `git log
--oneline origin/main..HEAD`, and the complete validation commands. Confirm no
runtime evidence, external URL, cache, credential, or personal path is tracked.

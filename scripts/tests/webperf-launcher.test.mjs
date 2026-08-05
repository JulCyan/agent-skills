import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import {
  chmod,
  cp,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  realpath,
  writeFile,
} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const testDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(testDir, '..', '..');
const skillRoot = path.join(repositoryRoot, 'skills', 'web-performance-lab');
const launcherPath = path.join(skillRoot, 'scripts', 'launcher.mjs');
const wrapperPath = path.join(skillRoot, 'scripts', 'webperf');

async function writeCapturingBinary(root) {
  const binary = path.join(root, 'trusted; webperf binary');
  await writeFile(
    binary,
    `#!/bin/sh
{
  printf 'cwd=%s\\n' "$PWD"
  for arg do
    printf 'arg=%s\\n' "$arg"
  done
} > "$WEBPERF_TEST_OUTPUT"
`,
  );
  await chmod(binary, 0o700);
  return binary;
}

test('webperf wrapper preserves caller cwd and arguments for the trusted override', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'webperf-launcher-override-'));
  const caller = path.join(root, 'caller with spaces');
  const output = path.join(root, 'result.txt');
  await mkdir(caller);
  const binary = await writeCapturingBinary(root);

  await execFileAsync(
    wrapperPath,
    ['profiles', 'list', 'literal;arg', '$(not-a-command)'],
    {
      cwd: caller,
      env: {
        ...process.env,
        WEBPERF_BINARY: binary,
        WEBPERF_TEST_OUTPUT: output,
      },
    },
  );

  const lines = (await readFile(output, 'utf8')).trim().split('\n');
  assert.equal(await realpath(lines[0].slice('cwd='.length)), await realpath(caller));
  assert.deepEqual(lines.slice(1), [
    'arg=profiles',
    'arg=list',
    'arg=literal;arg',
    'arg=$(not-a-command)',
  ]);
});

test('launcher without Go emits stable NEEDS_SETUP JSON and exit 2', async () => {
  await assert.rejects(
    execFileAsync(process.execPath, [launcherPath, '--json', 'doctor'], {
      env: { ...process.env, PATH: '' },
    }),
    (error) => {
      assert.equal(error.code, 2);
      assert.equal(error.stderr, '');
      assert.deepEqual(JSON.parse(error.stdout), {
        schemaVersion: 1,
        command: 'launcher',
        status: 'NEEDS_SETUP',
        error: {
          code: 'go_required',
          message: 'Go is required to build the bundled webperf CLI.',
          remediation: 'Install Go and retry, or set WEBPERF_BINARY to a trusted executable.',
        },
      });
      return true;
    },
  );
});

test('launcher setup failures stay path-free and machine-readable', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'webperf-launcher-failure-'));
  const unavailableTemporaryRoot = path.join(root, 'does-not-exist');
  await assert.rejects(
    execFileAsync(process.execPath, [launcherPath, '--json', 'doctor'], {
      env: { ...process.env, TMPDIR: unavailableTemporaryRoot },
    }),
    (error) => {
      assert.equal(error.code, 2);
      assert.equal(error.stderr, '');
      const report = JSON.parse(error.stdout);
      assert.equal(report.status, 'NEEDS_SETUP');
      assert.equal(report.error.code, 'launcher_failed');
      assert.doesNotMatch(
        error.stdout,
        new RegExp(root.replaceAll(/[.*+?^${}()|[\]\\]/g, '\\$&')),
      );
      return true;
    },
  );
});

test(
  'clean copied Skill builds with a controlled Go environment and removes its build directory',
  { timeout: 60_000 },
  async () => {
    const root = await mkdtemp(path.join(os.tmpdir(), 'webperf-launcher-copy-'));
    const copiedSkill = path.join(root, 'installed', 'web-performance-lab');
    const caller = path.join(root, 'caller');
    const temporaryRoot = path.join(root, 'executor-temporary');
    await mkdir(path.dirname(copiedSkill), { recursive: true });
    await mkdir(caller);
    await mkdir(temporaryRoot);
    await cp(skillRoot, copiedSkill, { recursive: true });

    const { stdout } = await execFileAsync(
      path.join(copiedSkill, 'scripts', 'webperf'),
      ['--json', 'profiles', 'list'],
      {
        cwd: caller,
        env: {
          ...process.env,
          CGO_ENABLED: '1',
          GOOS: 'windows',
          TMPDIR: temporaryRoot,
          GOENV: '/caller/must-not-control-goenv',
          GOFLAGS: '--caller-must-not-control-goflags',
          GOTELEMETRY: 'on',
          GOTOOLCHAIN: 'auto+path',
          GOTMPDIR: '/caller/must-not-control-gotmpdir',
          GOWORK: '/caller/must-not-control-gowork',
        },
        maxBuffer: 1024 * 1024,
      },
    );

    const report = JSON.parse(stdout);
    assert.equal(report.status, 'OK');
    assert.deepEqual(
      report.data.map((profile) => profile.name),
      ['desktop-observed-v1', 'desktop-lab-v1', 'mobile-lab-v1'],
    );
    assert.deepEqual(await readdir(temporaryRoot), []);
  },
);

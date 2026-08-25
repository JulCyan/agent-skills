import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { chmod, mkdtemp, readFile, realpath, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const testDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(testDir, '..', '..');
const launcherPath = path.join(
  repositoryRoot,
  'skills',
  'theme-template-sync',
  'scripts',
  'theme-template-sync.sh',
);

test('explicit trusted binary receives original cwd and arguments', async () => {
  const caller = await mkdtemp(path.join(os.tmpdir(), 'theme-template-launcher-cwd-'));
  const fakeBinary = path.join(caller, 'fake-template-sync');
  const output = path.join(caller, 'received.txt');
  await writeFile(
    fakeBinary,
    '#!/bin/sh\n{ pwd; printf "%s\\n" "$THEME_TEMPLATE_SYNC_CALLER_CWD"; printf "%s\\n" "$@"; } > "$TEST_OUTPUT"\n',
  );
  await chmod(fakeBinary, 0o700);

  await execFileAsync(launcherPath, ['plan', '--template', 'page.example'], {
    cwd: caller,
    env: {
      ...process.env,
      TEST_OUTPUT: output,
      THEME_TEMPLATE_SYNC_BINARY: fakeBinary,
      THEME_TEMPLATE_SYNC_CALLER_CWD: '/stale/caller/path',
    },
  });

  const lines = (await readFile(output, 'utf8')).trimEnd().split('\n');
  assert.equal(await realpath(lines[0]), await realpath(caller));
  assert.equal(await realpath(lines[1]), await realpath(caller));
  assert.deepEqual(lines.slice(2), ['plan', '--template', 'page.example']);
});

test('explicit trusted binary exit status is preserved', async () => {
  const caller = await mkdtemp(path.join(os.tmpdir(), 'theme-template-launcher-exit-'));
  const fakeBinary = path.join(caller, 'fake-template-sync');
  await writeFile(
    fakeBinary,
    '#!/bin/sh\nprintf \'%s\\n\' \'{"status":"PARTIAL"}\'\nprintf \'%s\\n\' \'synthetic partial\' >&2\nexit 4\n',
  );
  await chmod(fakeBinary, 0o700);

  await assert.rejects(
    execFileAsync(launcherPath, [], {
      cwd: caller,
      env: {
        ...process.env,
        THEME_TEMPLATE_SYNC_BINARY: fakeBinary,
      },
    }),
    (error) => {
      assert.equal(error.code, 4);
      assert.deepEqual(JSON.parse(error.stdout), { status: 'PARTIAL' });
      assert.equal(error.stderr, 'synthetic partial\n');
      return true;
    },
  );
});

test('bundled Go executor preserves application usage exit status', async () => {
  const caller = await mkdtemp(path.join(os.tmpdir(), 'theme-template-launcher-go-'));
  await assert.rejects(
    execFileAsync(launcherPath, ['destroy'], {
      cwd: caller,
      env: {
        ...process.env,
        GOFLAGS: '-synthetic-flag-must-not-be-inherited',
        GOTOOLCHAIN: 'synthetic-invalid-toolchain',
        GOWORK: '/stale/go.work',
        THEME_TEMPLATE_SYNC_BINARY: '',
      },
    }),
    (error) => {
      assert.equal(error.code, 2);
      const result = JSON.parse(error.stdout);
      assert.equal(result.status, 'FAILED');
      assert.equal(result.failure_kind, 'USAGE');
      assert.match(error.stderr, /unknown command/);
      assert.doesNotMatch(error.stderr, /exit status 2/);
      return true;
    },
  );
});

test('bundled Go executor redacts every value in a cookie header', async () => {
  const caller = await mkdtemp(path.join(os.tmpdir(), 'theme-template-launcher-redaction-'));
  const firstSecret = 'synthetic-cookie-first';
  const secondSecret = 'synthetic-cookie-second';
  await assert.rejects(
    execFileAsync(
      launcherPath,
      [`Cookie: first=${firstSecret}; session=${secondSecret}`],
      {
        cwd: caller,
        env: {
          ...process.env,
          THEME_TEMPLATE_SYNC_BINARY: '',
        },
      },
    ),
    (error) => {
      assert.equal(error.code, 2);
      for (const secret of [firstSecret, secondSecret]) {
        assert.doesNotMatch(error.stdout, new RegExp(secret));
        assert.doesNotMatch(error.stderr, new RegExp(secret));
      }
      assert.match(error.stdout, /\[redacted\]/);
      assert.match(error.stderr, /\[redacted\]/);
      return true;
    },
  );
});

test('bundled Go executor redacts every authorization parameter', async () => {
  const caller = await mkdtemp(path.join(os.tmpdir(), 'theme-template-launcher-auth-'));
  const secrets = [
    'synthetic-digest-user',
    'synthetic-digest-response',
    'synthetic-digest-nonce',
  ];
  const header =
    `Authorization: Digest username="${secrets[0]}", ` +
    `response="${secrets[1]}", nonce="${secrets[2]}"`;
  await assert.rejects(
    execFileAsync(launcherPath, [header], {
      cwd: caller,
      env: {
        ...process.env,
        THEME_TEMPLATE_SYNC_BINARY: '',
      },
    }),
    (error) => {
      assert.equal(error.code, 2);
      for (const secret of secrets) {
        assert.doesNotMatch(error.stdout, new RegExp(secret));
        assert.doesNotMatch(error.stderr, new RegExp(secret));
      }
      assert.match(error.stdout, /\[redacted\]/);
      assert.match(error.stderr, /\[redacted\]/);
      return true;
    },
  );
});

test('missing trusted binary and Go returns structured NEEDS_SETUP', async () => {
  await assert.rejects(
    execFileAsync(launcherPath, ['--help'], {
      env: {
        ...process.env,
        PATH: '',
        THEME_TEMPLATE_SYNC_BINARY: '',
      },
    }),
    (error) => {
      assert.equal(error.code, 2);
      assert.deepEqual(JSON.parse(error.stdout), {
        status: 'NEEDS_SETUP',
        reason: 'trusted-binary-and-go-missing',
      });
      assert.equal(error.stderr, '');
      return true;
    },
  );
});

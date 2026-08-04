import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { chmod, cp, mkdir, mkdtemp, readFile, realpath, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

import {
  resolveExecutor,
  sha256File,
  validateRuntimeManifest,
} from '../../skills/shopify-media-sync/scripts/launcher.mjs';

const execFileAsync = promisify(execFile);
const testDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(testDir, '..', '..');
const launcherPath = path.join(
  repositoryRoot,
  'skills',
  'shopify-media-sync',
  'scripts',
  'launcher.mjs',
);
const skillRoot = path.resolve(path.dirname(launcherPath), '..');

function unpublishedManifest() {
  return {
    schemaVersion: 1,
    skillVersion: '0.1.0-dev',
    repository: 'JulCyan/agent-skills',
    release: {
      status: 'unpublished',
      tag: 'shopify-media-sync-v0.1.0',
      assets: {},
    },
  };
}

test('unpublished manifest is valid but never resolves a download', async () => {
  const manifest = unpublishedManifest();
  validateRuntimeManifest(manifest);
  const result = await resolveExecutor({
    manifest,
    platform: 'darwin',
    arch: 'arm64',
    env: {},
    commandExists: async () => false,
  });
  assert.deepEqual(result, {
    kind: 'unavailable',
    reason: 'release-unpublished-and-go-missing',
  });
});

test('published manifest rejects latest and missing digests', () => {
  const latest = unpublishedManifest();
  latest.release = { status: 'published', tag: 'latest', assets: {} };
  assert.throws(() => validateRuntimeManifest(latest), /latest/);

  const missingDigest = unpublishedManifest();
  missingDigest.release = {
    status: 'published',
    tag: 'shopify-media-sync-v0.1.0',
    assets: {
      'darwin-arm64': { name: 'shopify-media-sync-darwin-arm64', sha256: '' },
    },
  };
  assert.throws(() => validateRuntimeManifest(missingDigest), /sha256/);
});

test('explicit binary has priority over release and Go fallback', async () => {
  const result = await resolveExecutor({
    manifest: unpublishedManifest(),
    platform: 'darwin',
    arch: 'arm64',
    env: { SHOPIFY_MEDIA_SYNC_BINARY: '/tmp/test-media-sync' },
    pathExists: async (candidate) => candidate === '/tmp/test-media-sync',
    commandExists: async () => true,
  });
  assert.deepEqual(result, { kind: 'binary', path: '/tmp/test-media-sync', source: 'explicit' });
});

test('valid cached release binary is selected and digest mismatch is rejected', async () => {
  const cacheRoot = await mkdtemp(path.join(os.tmpdir(), 'agent-skills-launcher-cache-'));
  const binaryPath = path.join(cacheRoot, 'shopify-media-sync-darwin-arm64');
  await writeFile(binaryPath, 'verified-binary');
  await chmod(binaryPath, 0o700);
  const digest = await sha256File(binaryPath);
  const manifest = unpublishedManifest();
  manifest.release = {
    status: 'published',
    tag: 'shopify-media-sync-v0.1.0',
    assets: {
      'darwin-arm64': { name: path.basename(binaryPath), sha256: digest },
    },
  };

  const resolved = await resolveExecutor({
    manifest,
    platform: 'darwin',
    arch: 'arm64',
    env: { SHOPIFY_MEDIA_SYNC_CACHE_DIR: cacheRoot },
    commandExists: async () => false,
    allowDownload: false,
  });
  assert.equal(resolved.source, 'release-cache');
  assert.equal(resolved.path, binaryPath);

  manifest.release.assets['darwin-arm64'].sha256 = '0'.repeat(64);
  await assert.rejects(
    resolveExecutor({
      manifest,
      platform: 'darwin',
      arch: 'arm64',
      env: { SHOPIFY_MEDIA_SYNC_CACHE_DIR: cacheRoot },
      commandExists: async () => false,
      allowDownload: false,
    }),
    /digest mismatch/,
  );
});

test('launcher preserves caller cwd and arguments for an explicit binary', async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'agent-skills-launcher-cwd-'));
  const fakeBinary = path.join(root, 'fake-media-sync');
  const output = path.join(root, 'result.json');
  await writeFile(
    fakeBinary,
    `#!/usr/bin/env node\nconst fs=require('node:fs');fs.writeFileSync(process.env.TEST_OUTPUT,JSON.stringify({cwd:process.cwd(),args:process.argv.slice(2)}));\n`,
  );
  await chmod(fakeBinary, 0o700);

  await execFileAsync(process.execPath, [launcherPath, 'plan', '--input', 'media.csv'], {
    cwd: root,
    env: {
      ...process.env,
      SHOPIFY_MEDIA_SYNC_BINARY: fakeBinary,
      TEST_OUTPUT: output,
    },
  });

  const result = JSON.parse(await readFile(output, 'utf8'));
  assert.equal(await realpath(result.cwd), await realpath(root));
  assert.deepEqual(result.args, ['plan', '--input', 'media.csv']);
});

test('unpublished launcher without Go returns structured NEEDS_SETUP', async () => {
  await assert.rejects(
    execFileAsync(process.execPath, [launcherPath, '--json', 'doctor'], {
      env: { ...process.env, PATH: '' },
    }),
    (error) => {
      assert.equal(error.code, 2);
      const report = JSON.parse(error.stdout);
      assert.equal(report.status, 'NEEDS_SETUP');
      assert.equal(report.reason, 'release-unpublished-and-go-missing');
      return true;
    },
  );
});

test(
  'clean copied Skill builds from source and runs help outside the repository',
  { timeout: 60_000 },
  async () => {
    const root = await mkdtemp(path.join(os.tmpdir(), 'agent-skills-clean-copy-'));
    const copiedSkill = path.join(root, 'installed', 'shopify-media-sync');
    const caller = path.join(root, 'caller');
    const cache = path.join(root, 'cache');
    await mkdir(path.dirname(copiedSkill), { recursive: true });
    await mkdir(caller, { recursive: true });
    await cp(skillRoot, copiedSkill, { recursive: true });

    const { stdout } = await execFileAsync(
      process.execPath,
      [path.join(copiedSkill, 'scripts', 'launcher.mjs'), '--help'],
      {
        cwd: caller,
        env: { ...process.env, SHOPIFY_MEDIA_SYNC_CACHE_DIR: cache },
        maxBuffer: 1024 * 1024,
      },
    );
    assert.match(stdout, /shopify-media-sync plans Shopify Files/);
  },
);

import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import {
  access,
  chmod,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
  writeFile,
} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

import { verifyInstalledSkill } from '../verify-installed-skill.mjs';

const execFileAsync = promisify(execFile);
const testDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(testDir, '..', '..');

async function doesNotExist(target) {
  try {
    await access(target);
    return false;
  } catch (error) {
    if (error.code === 'ENOENT') return true;
    throw error;
  }
}

async function runDoctor(wrapper, cwd, environment) {
  try {
    const result = await execFileAsync(wrapper, ['--json', 'doctor'], {
      cwd,
      env: environment,
      maxBuffer: 1024 * 1024,
    });
    return { code: 0, ...result };
  } catch (error) {
    if (error.code !== 3) throw error;
    return { code: error.code, stdout: error.stdout, stderr: error.stderr };
  }
}

function webperfRuntimeEnvironment(
  environment,
  { home, npmSentinel, runtimeBin, xdgCache },
) {
  const scrubbed = {
    ...environment,
    FORCE_COLOR: '0',
    HOME: home,
    NO_COLOR: '1',
    PATH: `${runtimeBin}${path.delimiter}${environment.PATH ?? ''}`,
    WEBPERF_NPM_TRAP_SENTINEL: npmSentinel,
    XDG_CACHE_HOME: xdgCache,
  };
  for (const key of Object.keys(scrubbed)) {
    if (key.startsWith('WEBPERF_INTERNAL_')) {
      delete scrubbed[key];
    }
  }
  return scrubbed;
}

test('repository declares explicit validation entrypoints for both product CLIs', async () => {
  const manifest = JSON.parse(await readFile(path.join(repositoryRoot, 'package.json'), 'utf8'));
  assert.deepEqual(
    {
      'test:go:media-sync': manifest.scripts['test:go:media-sync'],
      'test:go:webperf': manifest.scripts['test:go:webperf'],
      'test:shell:media-sync': manifest.scripts['test:shell:media-sync'],
      'test:shell:webperf': manifest.scripts['test:shell:webperf'],
      'test:go': manifest.scripts['test:go'],
      'test:shell': manifest.scripts['test:shell'],
    },
    {
      'test:go:media-sync':
        'cd skills/shopify-media-sync/scripts/media-sync-go && go test ./...',
      'test:go:webperf':
        'cd skills/web-performance-lab/scripts/webperf-go && go test ./...',
      'test:shell:media-sync':
        'sh -n skills/shopify-media-sync/scripts/shopify-media-sync.sh',
      'test:shell:webperf': 'sh -n skills/web-performance-lab/scripts/webperf',
      'test:go': 'npm run test:go:media-sync && npm run test:go:webperf',
      'test:shell': 'npm run test:shell:media-sync && npm run test:shell:webperf',
    },
  );
});

test(
  'tracked snapshot copy-installs webperf and runs read-only commands outside the provider',
  { timeout: 120_000 },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), 'webperf-install-'));
    t.after(() => rm(root, { recursive: true, force: true }));
    const archivePath = path.join(root, 'provider.tar');
    const providerExport = path.join(root, 'provider-export');
    const consumer = path.join(root, 'consumer');
    const caller = path.join(root, 'caller');
    const isolatedHome = path.join(root, 'runtime-home');
    const isolatedXDGCache = path.join(root, 'runtime-xdg-cache');
    const runtimeBin = path.join(root, 'runtime-bin');
    const npmSentinel = path.join(root, 'npm-invoked');
    await mkdir(providerExport);
    await mkdir(consumer);
    await mkdir(caller);
    await mkdir(isolatedHome);
    await mkdir(isolatedXDGCache);
    await mkdir(runtimeBin);
    const npmTrap = path.join(runtimeBin, 'npm');
    await writeFile(
      npmTrap,
      '#!/bin/sh\nprintf invoked > "$WEBPERF_NPM_TRAP_SENTINEL"\nexit 97\n',
    );
    await chmod(npmTrap, 0o700);

    const { stdout: indexPathOutput } = await execFileAsync(
      'git',
      ['rev-parse', '--git-path', 'index'],
      { cwd: repositoryRoot },
    );
    const isolatedIndex = path.join(root, 'tracked.index');
    await copyFile(path.resolve(repositoryRoot, indexPathOutput.trim()), isolatedIndex);
    const { stdout: stagedTree } = await execFileAsync('git', ['write-tree'], {
      cwd: repositoryRoot,
      env: { ...process.env, GIT_INDEX_FILE: isolatedIndex },
    });
    await execFileAsync(
      'git',
      ['archive', '--format=tar', '--output', archivePath, stagedTree.trim()],
      { cwd: repositoryRoot },
    );
    await execFileAsync('tar', ['-xf', archivePath, '-C', providerExport]);
    assert.equal(await doesNotExist(path.join(providerExport, '.git')), true);
    assert.equal(await doesNotExist(path.join(providerExport, 'skills-lock.json')), true);

    await execFileAsync('git', ['init', '--quiet'], { cwd: consumer });
    const installEnvironment = {
      ...process.env,
      NO_COLOR: '1',
      FORCE_COLOR: '0',
    };
    const { stdout: installStdout, stderr: installStderr } = await execFileAsync(
      'npx',
      ['--yes', 'skills@1.5.21', 'add', providerExport, '--all', '--copy'],
      {
        cwd: consumer,
        env: installEnvironment,
        maxBuffer: 4 * 1024 * 1024,
      },
    );
    const installOutput = `${installStdout}\n${installStderr}`;
    assert.match(installOutput, /Found 2 skills\b/);
    assert.match(installOutput, /Installed 2 skills\b/);
    assert.match(installOutput, /\bshopify-media-sync\b/);
    assert.match(installOutput, /\bweb-performance-lab\b/);
    assert.equal(
      (await verifyInstalledSkill({
        projectRoot: consumer,
        skillName: 'web-performance-lab',
      })).status,
      'MATCH',
    );

    const wrapper = path.join(
      consumer,
      '.agents',
      'skills',
      'web-performance-lab',
      'scripts',
      'webperf',
    );
    const runtimeEnvironment = webperfRuntimeEnvironment(process.env, {
      home: isolatedHome,
      npmSentinel,
      runtimeBin,
      xdgCache: isolatedXDGCache,
    });
    assert.equal(runtimeEnvironment.HOME, isolatedHome);
    assert.equal(runtimeEnvironment.XDG_CACHE_HOME, isolatedXDGCache);
    const { stdout: helpOutput } = await execFileAsync(wrapper, ['--help'], {
      cwd: caller,
      env: runtimeEnvironment,
      maxBuffer: 1024 * 1024,
    });
    assert.match(helpOutput, /webperf measures public web performance/);

    const doctorResult = await runDoctor(wrapper, caller, runtimeEnvironment);
    assert.equal(doctorResult.code, 3);
    const doctor = JSON.parse(doctorResult.stdout);
    assert.equal(doctor.schemaVersion, 1);
    assert.equal(doctor.command, 'doctor');
    assert.equal(doctor.status, 'NEEDS_SETUP');
    assert.equal(typeof doctor.data.goVersion, 'string');
    assert.equal(typeof doctor.data.os, 'string');
    assert.equal(typeof doctor.data.arch, 'string');
    assert.equal(doctor.data.engineStatus, 'NEEDS_SETUP');

    const { stdout: profilesOutput } = await execFileAsync(
      wrapper,
      ['--json', 'profiles', 'list'],
      { cwd: caller, env: runtimeEnvironment, maxBuffer: 1024 * 1024 },
    );
    const profiles = JSON.parse(profilesOutput);
    assert.equal(profiles.schemaVersion, 1);
    assert.equal(profiles.command, 'profiles list');
    assert.equal(profiles.status, 'OK');
    assert.deepEqual(
      profiles.data.map((profile) => profile.name),
      ['desktop-observed-v1', 'desktop-lab-v1', 'mobile-lab-v1'],
    );
    assert.deepEqual(await readdir(caller), []);
    assert.equal(await doesNotExist(npmSentinel), true);
    assert.equal(
      await doesNotExist(path.join(isolatedHome, 'Library', 'Caches', 'webperf')),
      true,
    );
    assert.equal(await doesNotExist(path.join(isolatedXDGCache, 'webperf')), true);
  },
);

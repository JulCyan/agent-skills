import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import {
  access,
  copyFile,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rm,
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

function webperfRuntimeEnvironment(environment) {
  const scrubbed = { ...environment, NO_COLOR: '1', FORCE_COLOR: '0' };
  for (const key of Object.keys(scrubbed)) {
    if (key === 'WEBPERF_BINARY' || key.startsWith('WEBPERF_INTERNAL_')) {
      delete scrubbed[key];
    }
  }
  return scrubbed;
}

test('repository declares explicit validation entrypoints for both product CLIs', async () => {
  const manifest = JSON.parse(await readFile(path.join(repositoryRoot, 'package.json'), 'utf8'));
  for (const script of [
    'test:go:media-sync',
    'test:go:webperf',
    'test:shell:media-sync',
    'test:shell:webperf',
  ]) {
    assert.equal(typeof manifest.scripts[script], 'string', `${script} must be declared`);
  }
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
    await mkdir(providerExport);
    await mkdir(consumer);
    await mkdir(caller);

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
    const runtimeEnvironment = webperfRuntimeEnvironment(process.env);
    const { stdout: helpOutput } = await execFileAsync(wrapper, ['--help'], {
      cwd: caller,
      env: runtimeEnvironment,
      maxBuffer: 1024 * 1024,
    });
    assert.match(helpOutput, /webperf measures public web performance/);

    const doctorResult = await runDoctor(wrapper, caller, runtimeEnvironment);
    assert.ok([0, 3].includes(doctorResult.code));
    const doctor = JSON.parse(doctorResult.stdout);
    assert.equal(doctor.schemaVersion, 1);
    assert.equal(doctor.command, 'doctor');
    assert.equal(doctor.status, doctorResult.code === 0 ? 'OK' : 'NEEDS_SETUP');
    assert.equal(typeof doctor.data.goVersion, 'string');
    assert.equal(typeof doctor.data.os, 'string');
    assert.equal(typeof doctor.data.arch, 'string');
    assert.ok(['OK', 'NEEDS_SETUP'].includes(doctor.data.engineStatus));

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
  },
);

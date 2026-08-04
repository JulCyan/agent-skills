import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

import {
  computeSkillFolderHash,
  verifyInstalledSkill,
} from '../verify-installed-skill.mjs';

const execFileAsync = promisify(execFile);
const scriptPath = fileURLToPath(new URL('../verify-installed-skill.mjs', import.meta.url));

async function temporaryDirectory(t, prefix = 'agent-skills-verifier-') {
  const directory = await mkdtemp(path.join(os.tmpdir(), prefix));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return directory;
}

async function writeTree(root, files) {
  for (const [relativePath, body] of Object.entries(files)) {
    const target = path.join(root, relativePath);
    await mkdir(path.dirname(target), { recursive: true });
    await writeFile(target, body);
  }
}

async function writeLock(projectRoot, skillName, computedHash) {
  await writeFile(
    path.join(projectRoot, 'skills-lock.json'),
    `${JSON.stringify({
      version: 1,
      skills: {
        [skillName]: {
          source: '/private/tmp/provider-export',
          sourceType: 'local',
          computedHash,
        },
      },
    })}\n`,
  );
}

async function runCLI(projectRoot, skillName) {
  try {
    const result = await execFileAsync(
      process.execPath,
      [scriptPath, '--project', projectRoot, '--skill', skillName],
      { maxBuffer: 1024 * 1024 },
    );
    return { code: 0, stdout: result.stdout, stderr: result.stderr };
  } catch (error) {
    return {
      code: error.code,
      stdout: error.stdout ?? '',
      stderr: error.stderr ?? '',
    };
  }
}

test('folder hash matches the pinned Skills CLI algorithm', async (t) => {
  const skillDir = await temporaryDirectory(t);
  await writeTree(skillDir, {
    'references/guide.md': 'beta\n',
    'SKILL.md': 'alpha\n',
  });

  assert.equal(
    await computeSkillFolderHash(skillDir),
    'a7e80930ba90c51c20ea38f912d35720af195b2fa3191ba9e1dd5ab129d27fee',
  );
});

test('folder hash ignores creation order, .git, and node_modules', async (t) => {
  const first = await temporaryDirectory(t, 'agent-skills-hash-first-');
  const second = await temporaryDirectory(t, 'agent-skills-hash-second-');
  await writeTree(first, {
    'SKILL.md': 'alpha\n',
    'references/guide.md': 'beta\n',
    '.git/config': 'ignored first\n',
    'node_modules/example/index.js': 'ignored first\n',
  });
  await writeTree(second, {
    'node_modules/example/index.js': 'ignored second\n',
    '.git/config': 'ignored second\n',
    'references/guide.md': 'beta\n',
    'SKILL.md': 'alpha\n',
  });

  assert.equal(await computeSkillFolderHash(first), await computeSkillFolderHash(second));
});

test('installed copy reports MATCH when its hash equals the lock', async (t) => {
  const projectRoot = await temporaryDirectory(t);
  const skillDir = path.join(projectRoot, '.agents', 'skills', 'example');
  await writeTree(skillDir, { 'SKILL.md': 'match\n' });
  const hash = await computeSkillFolderHash(skillDir);
  await writeLock(projectRoot, 'example', hash);

  assert.deepEqual(await verifyInstalledSkill({ projectRoot, skillName: 'example' }), {
    status: 'MATCH',
    skillName: 'example',
    expectedHash: hash,
    actualHash: hash,
  });
});

test('installed copy reports DRIFT after a tracked file is edited', async (t) => {
  const projectRoot = await temporaryDirectory(t);
  const skillDir = path.join(projectRoot, '.agents', 'skills', 'example');
  await writeTree(skillDir, { 'SKILL.md': 'before\n' });
  const expectedHash = await computeSkillFolderHash(skillDir);
  await writeLock(projectRoot, 'example', expectedHash);
  await writeFile(path.join(skillDir, 'SKILL.md'), 'after\n');
  const actualHash = await computeSkillFolderHash(skillDir);

  assert.deepEqual(await verifyInstalledSkill({ projectRoot, skillName: 'example' }), {
    status: 'DRIFT',
    skillName: 'example',
    reason: 'installed-copy-hash-mismatch',
    expectedHash,
    actualHash,
  });
});

test('missing installation prerequisites report specific NEEDS_SETUP reasons', async (t) => {
  const projectRoot = await temporaryDirectory(t);
  assert.deepEqual(await verifyInstalledSkill({ projectRoot, skillName: 'example' }), {
    status: 'NEEDS_SETUP',
    skillName: 'example',
    reason: 'lockfile-missing',
  });

  await writeFile(path.join(projectRoot, 'skills-lock.json'), '{bad json\n');
  assert.deepEqual(await verifyInstalledSkill({ projectRoot, skillName: 'example' }), {
    status: 'NEEDS_SETUP',
    skillName: 'example',
    reason: 'lockfile-invalid',
  });

  await writeFile(
    path.join(projectRoot, 'skills-lock.json'),
    '{"version":1,"skills":{}}\n',
  );
  assert.deepEqual(await verifyInstalledSkill({ projectRoot, skillName: 'example' }), {
    status: 'NEEDS_SETUP',
    skillName: 'example',
    reason: 'lock-entry-missing',
  });

  await writeLock(projectRoot, 'example', 'a'.repeat(64));
  assert.deepEqual(await verifyInstalledSkill({ projectRoot, skillName: 'example' }), {
    status: 'NEEDS_SETUP',
    skillName: 'example',
    reason: 'installed-copy-missing',
  });
});

test('CLI emits JSON with MATCH, DRIFT, and NEEDS_SETUP exit codes', async (t) => {
  const projectRoot = await temporaryDirectory(t);
  const missing = await runCLI(projectRoot, 'example');
  assert.equal(missing.code, 2);
  assert.equal(JSON.parse(missing.stdout).status, 'NEEDS_SETUP');
  assert.equal(missing.stderr, '');

  const skillDir = path.join(projectRoot, '.agents', 'skills', 'example');
  await writeTree(skillDir, { 'SKILL.md': 'before\n' });
  const expectedHash = await computeSkillFolderHash(skillDir);
  await writeLock(projectRoot, 'example', expectedHash);
  const match = await runCLI(projectRoot, 'example');
  assert.equal(match.code, 0);
  assert.equal(JSON.parse(match.stdout).status, 'MATCH');
  assert.equal(match.stderr, '');

  await writeFile(path.join(skillDir, 'SKILL.md'), 'after\n');
  const drift = await runCLI(projectRoot, 'example');
  assert.equal(drift.code, 1);
  assert.equal(JSON.parse(drift.stdout).status, 'DRIFT');
  assert.equal(drift.stderr, '');
});

test('CLI fails closed when required arguments are missing', async () => {
  let result;
  try {
    await execFileAsync(process.execPath, [scriptPath], { maxBuffer: 1024 * 1024 });
    assert.fail('CLI unexpectedly accepted missing arguments');
  } catch (error) {
    result = error;
  }

  assert.equal(result.code, 2);
  assert.deepEqual(JSON.parse(result.stdout), {
    status: 'NEEDS_SETUP',
    skillName: '',
    reason: 'invalid-arguments',
  });
  assert.equal(result.stderr, '');
});

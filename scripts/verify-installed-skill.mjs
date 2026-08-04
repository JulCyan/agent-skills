#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { readFile, readdir, realpath, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

async function collectFiles(baseDir, currentDir, results) {
  const entries = await readdir(currentDir, { withFileTypes: true });
  await Promise.all(
    entries.map(async (entry) => {
      const fullPath = path.join(currentDir, entry.name);
      if (entry.isDirectory()) {
        if (entry.name === '.git' || entry.name === 'node_modules') {
          return;
        }
        await collectFiles(baseDir, fullPath, results);
        return;
      }
      if (entry.isFile()) {
        results.push({
          relativePath: path.relative(baseDir, fullPath).split(path.sep).join('/'),
          content: await readFile(fullPath),
        });
      }
    }),
  );
}

export async function computeSkillFolderHash(skillDir) {
  const files = [];
  await collectFiles(skillDir, skillDir, files);
  files.sort((left, right) => left.relativePath.localeCompare(right.relativePath));

  const hash = createHash('sha256');
  for (const file of files) {
    hash.update(file.relativePath);
    hash.update(file.content);
  }
  return hash.digest('hex');
}

function needsSetup(skillName, reason) {
  return { status: 'NEEDS_SETUP', skillName, reason };
}

export async function verifyInstalledSkill({ projectRoot, skillName }) {
  if (!projectRoot || !skillName) {
    return needsSetup(skillName ?? '', 'invalid-arguments');
  }

  let lockBody;
  try {
    lockBody = await readFile(path.join(projectRoot, 'skills-lock.json'), 'utf8');
  } catch (error) {
    if (error.code === 'ENOENT') {
      return needsSetup(skillName, 'lockfile-missing');
    }
    throw error;
  }

  let lock;
  try {
    lock = JSON.parse(lockBody);
  } catch {
    return needsSetup(skillName, 'lockfile-invalid');
  }
  if (
    !lock ||
    !Number.isInteger(lock.version) ||
    !lock.skills ||
    Array.isArray(lock.skills) ||
    typeof lock.skills !== 'object'
  ) {
    return needsSetup(skillName, 'lockfile-invalid');
  }

  const entry = lock.skills[skillName];
  if (!entry || typeof entry.computedHash !== 'string') {
    return needsSetup(skillName, 'lock-entry-missing');
  }

  const skillDir = path.join(projectRoot, '.agents', 'skills', skillName);
  try {
    if (!(await stat(skillDir)).isDirectory()) {
      return needsSetup(skillName, 'installed-copy-missing');
    }
  } catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ENOTDIR') {
      return needsSetup(skillName, 'installed-copy-missing');
    }
    throw error;
  }

  const actualHash = await computeSkillFolderHash(skillDir);
  if (actualHash !== entry.computedHash) {
    return {
      status: 'DRIFT',
      skillName,
      reason: 'installed-copy-hash-mismatch',
      expectedHash: entry.computedHash,
      actualHash,
    };
  }
  return {
    status: 'MATCH',
    skillName,
    expectedHash: entry.computedHash,
    actualHash,
  };
}

function parseArguments(argv) {
  let projectRoot = '';
  let skillName = '';
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--project' && argv[index + 1]) {
      projectRoot = argv[index + 1];
      index += 1;
    } else if (argument === '--skill' && argv[index + 1]) {
      skillName = argv[index + 1];
      index += 1;
    } else {
      return { projectRoot: '', skillName: '' };
    }
  }
  return { projectRoot, skillName };
}

async function main() {
  const result = await verifyInstalledSkill(parseArguments(process.argv.slice(2)));
  process.stdout.write(`${JSON.stringify(result)}\n`);
  process.exitCode = result.status === 'MATCH' ? 0 : result.status === 'DRIFT' ? 1 : 2;
}

async function isMainModule() {
  if (!process.argv[1]) {
    return false;
  }
  try {
    return (
      (await realpath(fileURLToPath(import.meta.url))) === (await realpath(process.argv[1]))
    );
  } catch {
    return false;
  }
}

if (await isMainModule()) {
  await main();
}

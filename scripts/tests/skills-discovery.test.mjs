import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);
const testDir = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(testDir, '..', '..');

test(
  'npx skills discovery exposes both product Skills',
  { timeout: 60_000 },
  async () => {
    const { stdout, stderr } = await execFileAsync(
      'npx',
      ['--yes', 'skills@1.5.21', 'add', repositoryRoot, '--list'],
      {
        cwd: repositoryRoot,
        env: { ...process.env, NO_COLOR: '1', FORCE_COLOR: '0' },
        maxBuffer: 1024 * 1024,
      },
    );
    const output = `${stdout}\n${stderr}`;
    assert.match(output, /Found 2 skills\b/);
    assert.match(output, /\bshopify-media-sync\b/);
    assert.match(output, /\btheme-template-sync\b/);
    assert.doesNotMatch(output, /\bopenspec-/);
  },
);

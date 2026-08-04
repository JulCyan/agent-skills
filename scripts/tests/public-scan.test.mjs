import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { promisify } from 'node:util';

import { loadPolicy, scanGitIndex, scanMetadataText, scanPaths } from '../scan-public.mjs';

const execFileAsync = promisify(execFile);

const policyPath = new URL('../../config/public-scan-policy.json', import.meta.url);

async function fixture(files) {
  const root = await mkdtemp(path.join(os.tmpdir(), 'agent-skills-public-scan-'));
  for (const [relativePath, body] of Object.entries(files)) {
    const target = path.join(root, relativePath);
    await mkdir(path.dirname(target), { recursive: true });
    await writeFile(target, body);
  }
  return root;
}

test('clean generic public content passes', async () => {
  const root = await fixture({
    'skills/example/SKILL.md': 'Generic Shopify media workflow\n',
    'config/stores.config.json': '{"stores":[{"id":"store-us"}]}\n',
  });

  const findings = await scanPaths(root, await loadPolicy(policyPath));
  assert.deepEqual(findings, []);
});

test('operator-supplied forbidden token is matched without storing it in policy', async () => {
  const forbidden = 'example-sensitive-brand';
  const root = await fixture({ 'README.md': `owner=${forbidden}\n` });
  const policyBody = await readFile(policyPath, 'utf8');

  const findings = await scanPaths(root, await loadPolicy(policyPath), {
    forbiddenTokens: [forbidden],
  });
  assert.equal(findings.length, 1);
  assert.equal(findings[0].rule, 'operator-forbidden-token');
  assert.equal(findings[0].path, 'README.md');
  assert.ok(!JSON.stringify(findings).includes(forbidden));
  assert.ok(!policyBody.includes(forbidden));
});

test('operator forbidden values match multi-word content and tracked path names', async () => {
  const forbidden = 'private project name';
  const root = await fixture({
    'notes/content.md': `owned by ${forbidden}\n`,
    'notes/private project name/placeholder.md': 'generic\n',
  });
  const findings = await scanPaths(root, await loadPolicy(policyPath), {
    forbiddenTokens: [forbidden],
  });
  assert.deepEqual(findings, [
    { path: 'notes/content.md', rule: 'operator-forbidden-token' },
    { path: 'notes/private project name/placeholder.md', rule: 'operator-forbidden-token' },
  ]);
});

test('Git index scan includes tracked files under filesystem-ignored directories', async () => {
  const forbidden = 'private-project';
  const root = await fixture({ '.cache/tracked.txt': `${forbidden}\n` });
  await execFileAsync('git', ['init', '--quiet'], { cwd: root });
  await execFileAsync('git', ['add', '-f', '.cache/tracked.txt'], { cwd: root });

  const findings = await scanGitIndex(root, await loadPolicy(policyPath), {
    forbiddenTokens: [forbidden],
  });
  assert.deepEqual(findings, [
    { path: '.cache/tracked.txt', rule: 'operator-forbidden-token' },
  ]);
});

test('external PR metadata is scanned without treating SSH remotes as email', async () => {
  const forbidden = 'private-project';
  const findings = scanMetadataText(
    '<github-event>',
    JSON.stringify({
      title: `review ${forbidden}`,
      ssh_url: ['git', 'github.com:Example/repo.git'].join('@'),
    }),
    await loadPolicy(policyPath),
    [forbidden],
  );
  assert.deepEqual(findings, [
    { path: '<github-event>', rule: 'operator-forbidden-token' },
  ]);
});

test('credential-shaped values are rejected without echoing the value', async () => {
  const shaped = ['sh', 'pat', '_do-not-store-provider-shaped-fixture'].join('');
  const root = await fixture({ 'testdata/token.txt': `${shaped}\n` });

  const findings = await scanPaths(root, await loadPolicy(policyPath));
  assert.equal(findings[0].rule, 'credential-pattern');
  assert.ok(!JSON.stringify(findings).includes(shaped));
});

test('personal absolute paths, real-looking target ids, and internal evidence roots are rejected', async () => {
  const personalPath = ['/Us', 'ers/example/work/project'].join('');
  const internalRoot = ['.tre', 'llis/tasks/change/evidence.json'].join('');
  const targetID = ['preview_theme_id=', '1829', '00000001'].join('');
  const root = await fixture({
    'notes/path.txt': personalPath,
    'notes/target.txt': targetID,
    'notes/evidence.txt': internalRoot,
  });

  const findings = await scanPaths(root, await loadPolicy(policyPath));
  assert.deepEqual(
    findings.map((finding) => finding.rule).sort(),
    ['internal-evidence-path', 'personal-absolute-path', 'real-target-id'],
  );
});

test('provider rejects a local-source skills lock', async () => {
  const root = await fixture({
    'skills-lock.json': JSON.stringify({
      version: 1,
      skills: {
        example: {
          source: '/private/tmp/provider-export',
          sourceType: 'local',
          computedHash: 'a'.repeat(64),
        },
      },
    }),
  });

  const findings = await scanPaths(root, await loadPolicy(policyPath));
  assert.deepEqual(findings, [
    { path: 'skills-lock.json', rule: 'provider-local-skill-lock' },
  ]);
});

test('provider accepts a reconstructable GitHub skills lock', async () => {
  const root = await fixture({
    'skills-lock.json': JSON.stringify({
      version: 1,
      skills: {
        example: {
          source: 'ExampleOrg/example-skills',
          sourceType: 'github',
          sourceUrl: 'https://github.com/ExampleOrg/example-skills.git',
          ref: 'v1.2.3',
          skillPath: 'skills/example',
          computedHash: 'b'.repeat(64),
        },
      },
    }),
  });

  assert.deepEqual(await scanPaths(root, await loadPolicy(policyPath)), []);
});

test('provider rejects a malformed skills lock', async () => {
  const root = await fixture({ 'skills-lock.json': '{"version":1}' });

  const findings = await scanPaths(root, await loadPolicy(policyPath));
  assert.deepEqual(findings, [
    { path: 'skills-lock.json', rule: 'invalid-project-skill-lock' },
  ]);
});

import assert from 'node:assert/strict';
import { mkdtemp, mkdir, readFile, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import { loadPolicy, scanPaths } from '../scan-public.mjs';

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

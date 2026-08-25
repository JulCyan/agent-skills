import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { access, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
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
    if (error.code === 'ENOENT') {
      return true;
    }
    throw error;
  }
}

function scrubShopifyCredentials(environment) {
  const scrubbed = { ...environment };
  for (const key of [
    'SHOPIFY_ACCESS_TOKEN',
    'SHOPIFY_ADMIN_ACCESS_TOKEN',
    'SHOPIFY_API_KEY',
    'SHOPIFY_API_SECRET',
    'SHOPIFY_CLIENT_ID',
    'SHOPIFY_CLIENT_SECRET',
    'SHOPIFY_STORE',
    'THEME_TEMPLATE_SYNC_BINARY',
    'THEME_TEMPLATE_SYNC_CALLER_CWD',
  ]) {
    delete scrubbed[key];
  }
  return scrubbed;
}

test(
  'tracked snapshot installs two self-contained Skills into a disposable sandbox',
  { timeout: 120_000 },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), 'agent-skills-sandbox-install-'));
    t.after(() => rm(root, { recursive: true, force: true }));
    const archivePath = path.join(root, 'provider.tar');
    const providerExport = path.join(root, 'provider-export');
    const consumer = path.join(root, 'consumer');
    const caller = path.join(root, 'caller');
    const cache = path.join(root, 'cache');
    await mkdir(providerExport);
    await mkdir(consumer);
    await mkdir(caller);

    const { stdout: stagedTree } = await execFileAsync('git', ['write-tree'], {
      cwd: repositoryRoot,
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
    assert.match(installOutput, /\btheme-template-sync\b/);

    const lock = JSON.parse(await readFile(path.join(consumer, 'skills-lock.json'), 'utf8'));
    const entry = lock.skills['shopify-media-sync'];
    assert.equal(lock.version, 1);
    assert.equal(entry.sourceType, 'local');
    assert.match(entry.computedHash, /^[a-f0-9]{64}$/);
    assert.equal(
      (await verifyInstalledSkill({
        projectRoot: consumer,
        skillName: 'shopify-media-sync',
      })).status,
      'MATCH',
    );
    const templateEntry = lock.skills['theme-template-sync'];
    assert.match(templateEntry.computedHash, /^[a-f0-9]{64}$/);
    assert.equal(
      (await verifyInstalledSkill({
        projectRoot: consumer,
        skillName: 'theme-template-sync',
      })).status,
      'MATCH',
    );

    const wrapper = path.join(
      consumer,
      '.agents',
      'skills',
      'shopify-media-sync',
      'scripts',
      'shopify-media-sync.sh',
    );
    const runtimeEnvironment = scrubShopifyCredentials({
      ...process.env,
      SHOPIFY_MEDIA_SYNC_CACHE_DIR: cache,
    });
    const { stdout: helpOutput } = await execFileAsync(wrapper, ['--help'], {
      cwd: caller,
      env: runtimeEnvironment,
      maxBuffer: 1024 * 1024,
    });
    assert.match(helpOutput, /shopify-media-sync plans Shopify Files/);

    const { stdout: doctorOutput } = await execFileAsync(wrapper, ['--json', 'doctor'], {
      cwd: caller,
      env: runtimeEnvironment,
      maxBuffer: 1024 * 1024,
    });
    const report = JSON.parse(doctorOutput);
    assert.equal(report.command, 'doctor');
    assert.equal(report.capabilities.local_input.available, true);

    await mkdir(path.join(caller, 'images'));
    await writeFile(
      path.join(caller, 'stores.config.json'),
      `${JSON.stringify({
        stores: [
          {
            id: 'store-demo',
            label: 'Demo',
            shopifyStore: 'store-demo',
            primaryLocale: 'en',
            enabled: true,
          },
        ],
      })}\n`,
    );
    await writeFile(
      path.join(caller, 'media.csv'),
      '序号,source图片名,target图片文件名,en\n1,asset.svg,asset.svg,Synthetic alt\n',
    );
    await writeFile(
      path.join(caller, 'images', 'asset.svg'),
      '<svg xmlns="http://www.w3.org/2000/svg" width="2" height="2"></svg>\n',
    );

    await execFileAsync(
      wrapper,
      [
        'plan',
        '--input',
        'media.csv',
        '--source-root',
        'images',
        '--stores',
        'all',
        '--out-dir',
        'run',
      ],
      { cwd: caller, env: runtimeEnvironment, maxBuffer: 1024 * 1024 },
    );
    const { stdout: inspectOutput } = await execFileAsync(
      wrapper,
      ['inspect', '--plan', 'run/plan.json', '--format', 'json'],
      { cwd: caller, env: runtimeEnvironment, maxBuffer: 1024 * 1024 },
    );
    const inspection = JSON.parse(inspectOutput);
    assert.equal(inspection.run_id.length > 0, true);
    assert.match(inspection.plan_path, /run\/plan\.json$/);
    assert.match(inspection.plan_sha256, /^[a-f0-9]{64}$/);

    const templateWrapper = path.join(
      consumer,
      '.agents',
      'skills',
      'theme-template-sync',
      'scripts',
      'theme-template-sync.sh',
    );
    const { stdout: templateHelp } = await execFileAsync(templateWrapper, ['--help'], {
      cwd: caller,
      env: runtimeEnvironment,
      maxBuffer: 1024 * 1024,
    });
    assert.match(templateHelp, /theme-template-sync safely synchronizes Shopify JSON templates/);
    assert.equal(
      await doesNotExist(
        path.join(consumer, '.agents', 'skills', 'theme-template-sync', '.runtime'),
      ),
      true,
    );
  },
);

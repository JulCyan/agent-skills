#!/usr/bin/env node

import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  access,
  chmod,
  mkdir,
  readFile,
  realpath,
  rename,
  rm,
  writeFile,
} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const executableAccess = process.platform === 'win32' ? 0 : 1;

export function validateRuntimeManifest(manifest) {
  if (!manifest || manifest.schemaVersion !== 1) {
    throw new Error('runtime manifest schemaVersion must be 1');
  }
  if (!/^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-z0-9.-]+)?$/.test(manifest.skillVersion ?? '')) {
    throw new Error('runtime manifest skillVersion is invalid');
  }
  if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(manifest.repository ?? '')) {
    throw new Error('runtime manifest repository must be owner/name');
  }

  const release = manifest.release;
  if (!release || !['unpublished', 'published'].includes(release.status)) {
    throw new Error('runtime manifest release status is invalid');
  }
  if (!release.tag || release.tag.toLowerCase() === 'latest') {
    throw new Error('runtime manifest must use a fixed release tag, never latest');
  }
  if (!release.assets || typeof release.assets !== 'object' || Array.isArray(release.assets)) {
    throw new Error('runtime manifest release assets must be an object');
  }
  if (release.status === 'unpublished' && Object.keys(release.assets).length > 0) {
    throw new Error('unpublished runtime manifest must not advertise assets');
  }
  for (const [platform, asset] of Object.entries(release.assets)) {
    if (!/^(darwin|linux|win32)-(arm64|x64)$/.test(platform)) {
      throw new Error(`runtime manifest platform ${platform} is unsupported`);
    }
    if (!asset || path.basename(asset.name ?? '') !== asset.name || asset.name.length === 0) {
      throw new Error(`runtime manifest asset name for ${platform} is invalid`);
    }
    if (!/^[a-f0-9]{64}$/.test(asset.sha256 ?? '')) {
      throw new Error(`runtime manifest sha256 for ${platform} is invalid`);
    }
  }
}

export async function sha256File(filePath) {
  const body = await readFile(filePath);
  return createHash('sha256').update(body).digest('hex');
}

async function defaultPathExists(candidate) {
  try {
    await access(candidate, executableAccess);
    return true;
  } catch {
    return false;
  }
}

async function defaultCommandExists(command) {
  const result = spawnSync(command, ['version'], { stdio: 'ignore' });
  return result.status === 0;
}

function defaultCacheRoot(env, manifest) {
  if (env.SHOPIFY_MEDIA_SYNC_CACHE_DIR) {
    return path.resolve(env.SHOPIFY_MEDIA_SYNC_CACHE_DIR);
  }
  return path.join(os.homedir(), '.cache', 'agent-skills', 'shopify-media-sync', manifest.skillVersion);
}

async function verifyCachedBinary(binaryPath, expectedDigest) {
  const digest = await sha256File(binaryPath);
  if (digest !== expectedDigest) {
    throw new Error(`cached executor digest mismatch at ${binaryPath}`);
  }
  return binaryPath;
}

async function downloadReleaseBinary({ manifest, asset, cacheRoot, fetchImpl }) {
  await mkdir(cacheRoot, { recursive: true, mode: 0o700 });
  const target = path.join(cacheRoot, asset.name);
  const temporary = `${target}.download-${process.pid}`;
  const url = `https://github.com/${manifest.repository}/releases/download/${encodeURIComponent(
    manifest.release.tag,
  )}/${encodeURIComponent(asset.name)}`;

  const response = await fetchImpl(url, { redirect: 'follow' });
  if (!response.ok) {
    throw new Error(`executor download failed with HTTP ${response.status}`);
  }
  const body = Buffer.from(await response.arrayBuffer());
  await writeFile(temporary, body, { mode: 0o700 });
  try {
    await verifyCachedBinary(temporary, asset.sha256);
    await chmod(temporary, 0o700);
    await rename(temporary, target);
  } finally {
    await rm(temporary, { force: true });
  }
  return target;
}

export async function resolveExecutor(options) {
  const {
    manifest,
    platform = process.platform,
    arch = process.arch,
    env = process.env,
    pathExists = defaultPathExists,
    commandExists = defaultCommandExists,
    allowDownload = true,
    fetchImpl = globalThis.fetch,
  } = options;
  validateRuntimeManifest(manifest);

  if (env.SHOPIFY_MEDIA_SYNC_BINARY) {
    const explicitPath = path.resolve(env.SHOPIFY_MEDIA_SYNC_BINARY);
    if (!(await pathExists(explicitPath))) {
      throw new Error('SHOPIFY_MEDIA_SYNC_BINARY is not executable');
    }
    return { kind: 'binary', path: explicitPath, source: 'explicit' };
  }

  const cacheRoot = defaultCacheRoot(env, manifest);
  const platformKey = `${platform}-${arch}`;
  const asset = manifest.release.assets[platformKey];
  if (manifest.release.status === 'published' && asset) {
    const cachedPath = path.join(cacheRoot, asset.name);
    if (await pathExists(cachedPath)) {
      await verifyCachedBinary(cachedPath, asset.sha256);
      return { kind: 'binary', path: cachedPath, source: 'release-cache' };
    }
    if (allowDownload) {
      if (typeof fetchImpl !== 'function') {
        throw new Error('executor download requires fetch support');
      }
      const downloadedPath = await downloadReleaseBinary({
        manifest,
        asset,
        cacheRoot,
        fetchImpl,
      });
      return { kind: 'binary', path: downloadedPath, source: 'release-download' };
    }
  }

  if (await commandExists('go')) {
    return { kind: 'source-build', cacheRoot };
  }

  if (manifest.release.status === 'unpublished') {
    return { kind: 'unavailable', reason: 'release-unpublished-and-go-missing' };
  }
  if (!asset) {
    return { kind: 'unavailable', reason: 'unsupported-platform-and-go-missing' };
  }
  return { kind: 'unavailable', reason: 'release-cache-miss-and-go-missing' };
}

async function spawnAndWait(command, args, options = {}) {
  const writeAll = async (source, target) => {
    if (!source) {
      return;
    }
    for await (const chunk of source) {
      await new Promise((resolve, reject) => {
        target.write(chunk, (error) => (error ? reject(error) : resolve()));
      });
    }
  };

  const child = spawn(command, args, {
    stdio: ['inherit', 'pipe', 'pipe'],
    ...options,
  });
  const completed = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code, signal) => resolve({ code, signal }));
  });
  const [result] = await Promise.all([
    completed,
    writeAll(child.stdout, process.stdout),
    writeAll(child.stderr, process.stderr),
  ]);
  return result;
}

async function buildSourceExecutor({ scriptDir, cacheRoot }) {
  const moduleRoot = path.join(scriptDir, 'media-sync-go');
  const buildRoot = path.join(cacheRoot, 'source-build');
  await mkdir(buildRoot, { recursive: true, mode: 0o700 });
  const suffix = process.platform === 'win32' ? '.exe' : '';
  const temporary = path.join(buildRoot, `.shopify-media-sync-${process.pid}${suffix}`);
  const result = await spawnAndWait(
    'go',
    ['build', '-trimpath', '-buildvcs=false', '-o', temporary, './cmd/shopify-media-sync'],
    { cwd: moduleRoot },
  );
  if (result.code !== 0) {
    throw new Error(`go build failed with exit ${result.code ?? result.signal}`);
  }
  try {
    const digest = await sha256File(temporary);
    const target = path.join(buildRoot, `shopify-media-sync-${digest}${suffix}`);
    await chmod(temporary, 0o700);
    await rename(temporary, target).catch(async (error) => {
      if (await defaultPathExists(target)) {
        return;
      }
      throw error;
    });
    return target;
  } finally {
    await rm(temporary, { force: true });
  }
}

function printUnavailable(reason) {
  const report = {
    schema_version: 1,
    command: 'launcher',
    status: 'NEEDS_SETUP',
    runtime: {
      node: process.version,
      platform: process.platform,
      arch: process.arch,
    },
    reason,
    next_action:
      'use an approved published Skill release, set SHOPIFY_MEDIA_SYNC_BINARY to a trusted executor, or install the Go toolchain for source fallback',
  };
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
}

async function main() {
  const scriptDir = path.dirname(fileURLToPath(import.meta.url));
  const manifestPath = path.resolve(scriptDir, '..', 'runtime.json');
  const manifest = JSON.parse(await readFile(manifestPath, 'utf8'));
  const resolved = await resolveExecutor({ manifest });
  if (resolved.kind === 'unavailable') {
    printUnavailable(resolved.reason);
    process.exitCode = 2;
    return;
  }

  const binaryPath =
    resolved.kind === 'source-build'
      ? await buildSourceExecutor({ scriptDir, cacheRoot: resolved.cacheRoot })
      : resolved.path;
  const result = await spawnAndWait(binaryPath, process.argv.slice(2), {
    cwd: process.cwd(),
    env: process.env,
  });
  if (result.signal) {
    process.kill(process.pid, result.signal);
    return;
  }
  process.exitCode = result.code ?? 1;
}

async function isMainModule() {
  if (!process.argv[1]) {
    return false;
  }
  try {
    return (await realpath(fileURLToPath(import.meta.url))) === (await realpath(process.argv[1]));
  } catch {
    return false;
  }
}

if (await isMainModule()) {
  await main();
}

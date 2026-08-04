#!/usr/bin/env node

import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const credentialPatterns = [
  new RegExp(['sh', 'pat', '_[A-Za-z0-9_-]{8,}'].join(''), 'g'),
  new RegExp(['sh', 'pss', '_[A-Za-z0-9_-]{8,}'].join(''), 'g'),
  new RegExp(['gh', '[pousr]', '_[A-Za-z0-9]{20,}'].join(''), 'g'),
  new RegExp(['xox', '[baprs]', '-[A-Za-z0-9-]{10,}'].join(''), 'g'),
  new RegExp(['AIza', '[A-Za-z0-9_-]{20,}'].join(''), 'g'),
  new RegExp(['-----BEGIN ', '(?:RSA |OPENSSH |EC |DSA )?', 'PRIVATE KEY-----'].join(''), 'g'),
];

const personalPathPatterns = [
  new RegExp(['/', 'Users', '/[^/\\s]+/'].join(''), 'g'),
  new RegExp(['[A-Za-z]:\\\\', 'Users', '\\\\[^\\\\\\s]+\\\\'].join(''), 'g'),
];

const targetIDPatterns = [
  /(?:preview_theme_id|theme[_-]?id|store[_-]?id)\s*[=:]\s*["']?\d{10,}/gi,
  /gid:\/\/shopify\/(?:Product|MediaImage|Video|GenericFile)\/\d{8,}/g,
];

const internalEvidencePatterns = [
  new RegExp(['\\.', 'tre', 'llis', '/'].join(''), 'g'),
  new RegExp(['\\.', 'spec', 'ify', '/'].join(''), 'g'),
  new RegExp(['\\.', 'agent-', 'runtime', '/'].join(''), 'g'),
];

const tokenPattern = /[\p{L}\p{N}][\p{L}\p{N}_-]*/gu;
const emailPattern = /\b[A-Z0-9._%+-]+@([A-Z0-9.-]+\.[A-Z]{2,})\b/gi;

export async function loadPolicy(policyURL) {
  const raw = await readFile(policyURL, 'utf8');
  const policy = JSON.parse(raw);
  if (
    policy.schemaVersion !== 2 ||
    typeof policy.forbiddenTokenEnvironment !== 'string' ||
    policy.forbiddenTokenEnvironment.length === 0
  ) {
    throw new Error('public scan policy schema is invalid');
  }
  return policy;
}

async function collectFiles(root, ignoredDirectories, relative = '') {
  const directory = path.join(root, relative);
  const entries = await readdir(directory, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const child = relative ? path.join(relative, entry.name) : entry.name;
    if (entry.name === '.git') {
      continue;
    }
    if (entry.isDirectory()) {
      if (!ignoredDirectories.has(entry.name)) {
        files.push(...(await collectFiles(root, ignoredDirectories, child)));
      }
      continue;
    }
    if (entry.isFile()) {
      files.push(child);
    }
  }
  return files;
}

function addFinding(findings, relativePath, rule) {
  if (!findings.some((finding) => finding.path === relativePath && finding.rule === rule)) {
    findings.push({ path: relativePath.split(path.sep).join('/'), rule });
  }
}

function containsPattern(body, patterns) {
  return patterns.some((pattern) => {
    pattern.lastIndex = 0;
    return pattern.test(body);
  });
}

function isSensitiveFilename(relativePath) {
  const base = path.basename(relativePath).toLowerCase();
  if (base.startsWith('.env') && base !== '.env.example') {
    return true;
  }
  return /(?:^|[._-])(?:credentials?|secrets?|private[-_]?key)(?:[._-]|$)/i.test(base);
}

function looksBinary(buffer) {
  const sample = buffer.subarray(0, Math.min(buffer.length, 8192));
  return sample.includes(0);
}

function scanProjectSkillLock(relativePath, body, findings) {
  if (relativePath.split(path.sep).join('/') !== 'skills-lock.json') {
    return;
  }

  let lock;
  try {
    lock = JSON.parse(body);
  } catch {
    addFinding(findings, relativePath, 'invalid-project-skill-lock');
    return;
  }

  if (
    !lock ||
    !Number.isInteger(lock.version) ||
    lock.version < 1 ||
    !lock.skills ||
    Array.isArray(lock.skills) ||
    typeof lock.skills !== 'object'
  ) {
    addFinding(findings, relativePath, 'invalid-project-skill-lock');
    return;
  }

  for (const entry of Object.values(lock.skills)) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      addFinding(findings, relativePath, 'invalid-project-skill-lock');
      return;
    }
    const source = typeof entry.source === 'string' ? entry.source : '';
    const sourceURL = typeof entry.sourceUrl === 'string' ? entry.sourceUrl : '';
    if (
      entry.sourceType === 'local' ||
      [source, sourceURL].some(
        (value) =>
          value.startsWith('/') ||
          value.startsWith('./') ||
          value.startsWith('../') ||
          value.startsWith('file:'),
      )
    ) {
      addFinding(findings, relativePath, 'provider-local-skill-lock');
      return;
    }
  }
}

function scanBody(relativePath, body, policy, forbiddenTokens, findings) {
  const forbidden = new Set(
    forbiddenTokens.map((value) => value.normalize('NFKC').toLowerCase()).filter(Boolean),
  );
  for (const match of body.matchAll(tokenPattern)) {
    const normalized = match[0].normalize('NFKC').toLowerCase();
    if (forbidden.has(normalized)) {
      addFinding(findings, relativePath, 'operator-forbidden-token');
      break;
    }
  }

  if (containsPattern(body, credentialPatterns)) {
    addFinding(findings, relativePath, 'credential-pattern');
  }
  if (containsPattern(body, personalPathPatterns)) {
    addFinding(findings, relativePath, 'personal-absolute-path');
  }
  if (containsPattern(body, targetIDPatterns)) {
    addFinding(findings, relativePath, 'real-target-id');
  }
  if (containsPattern(body, internalEvidencePatterns)) {
    addFinding(findings, relativePath, 'internal-evidence-path');
  }

  const allowedDomains = new Set((policy.allowedEmailDomains ?? []).map((value) => value.toLowerCase()));
  for (const match of body.matchAll(emailPattern)) {
    if (!allowedDomains.has(match[1].toLowerCase())) {
      addFinding(findings, relativePath, 'personal-email');
      break;
    }
  }

  scanProjectSkillLock(relativePath, body, findings);
}

function parseForbiddenTokens(value) {
  return value
    .split(/[\n,]/u)
    .map((token) => token.trim())
    .filter(Boolean);
}

export async function scanPaths(root, policy, options = {}) {
  const absoluteRoot = path.resolve(root);
  const ignoredDirectories = new Set(policy.ignoredDirectories ?? []);
  const forbiddenTokens =
    options.forbiddenTokens ??
    parseForbiddenTokens(process.env[policy.forbiddenTokenEnvironment] ?? '');
  const files = await collectFiles(absoluteRoot, ignoredDirectories);
  const findings = [];

  for (const relativePath of files.sort()) {
    if (isSensitiveFilename(relativePath)) {
      addFinding(findings, relativePath, 'forbidden-sensitive-file');
      continue;
    }
    const buffer = await readFile(path.join(absoluteRoot, relativePath));
    if (!looksBinary(buffer)) {
      scanBody(relativePath, buffer.toString('utf8'), policy, forbiddenTokens, findings);
    }
  }

  return findings.sort((left, right) =>
    `${left.path}:${left.rule}`.localeCompare(`${right.path}:${right.rule}`),
  );
}

async function main() {
  const scriptDir = path.dirname(fileURLToPath(import.meta.url));
  const policyPath = path.resolve(scriptDir, '..', 'config', 'public-scan-policy.json');
  const rootIndex = process.argv.indexOf('--root');
  const root = rootIndex >= 0 ? process.argv[rootIndex + 1] : process.cwd();
  if (!root) {
    throw new Error('--root requires a path');
  }

  const findings = await scanPaths(root, await loadPolicy(pathToFileURL(policyPath)));
  if (findings.length > 0) {
    process.stderr.write(`${JSON.stringify({ status: 'BLOCKED', findings }, null, 2)}\n`);
    process.exitCode = 1;
    return;
  }
  process.stdout.write(`${JSON.stringify({ status: 'PASS', findings: [] })}\n`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  await main();
}

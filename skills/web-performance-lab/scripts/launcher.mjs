#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { mkdir, mkdtemp, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptsDirectory = path.dirname(fileURLToPath(import.meta.url));
const sourceDirectory = path.join(scriptsDirectory, 'webperf-go');
const forwardedSignals = ['SIGINT', 'SIGTERM'];

function controlledGoEnvironment(buildDirectory) {
  const compilerControls = new Set(['ar', 'cc', 'cxx', 'gccgo', 'pkg_config']);
  const environment = Object.fromEntries(
    Object.entries(process.env).filter(([name]) => {
      const lowerName = name.toLowerCase();
      return (
        !lowerName.startsWith('go') &&
        !lowerName.startsWith('cgo_') &&
        !compilerControls.has(lowerName)
      );
    }),
  );
  return {
    ...environment,
    CGO_ENABLED: '0',
    GO111MODULE: 'on',
    GOCACHE: path.join(buildDirectory, 'go-build-cache'),
    GOENV: 'off',
    GOFLAGS: '',
    GOMODCACHE: path.join(buildDirectory, 'go-module-cache'),
    GONOPROXY: 'none',
    GONOSUMDB: '*',
    GOPATH: path.join(buildDirectory, 'go-path'),
    GOPRIVATE: '',
    GOPROXY: 'off',
    GOSUMDB: 'off',
    GOTELEMETRY: 'off',
    GOTOOLCHAIN: 'local',
    GOTMPDIR: path.join(buildDirectory, 'go-temporary'),
    GOVCS: '*:off',
    GOWORK: 'off',
  };
}

function signalExitCode(signal) {
  if (signal === 'SIGINT') return 130;
  if (signal === 'SIGTERM') return 143;
  return 1;
}

function run(command, args, options) {
  return new Promise((resolve) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env,
      shell: false,
      stdio: options.stdio,
    });
    let settled = false;
    const handlers = new Map();
    for (const signal of forwardedSignals) {
      const handler = () => child.kill(signal);
      handlers.set(signal, handler);
      process.once(signal, handler);
    }
    const finish = (result) => {
      if (settled) return;
      settled = true;
      for (const [signal, handler] of handlers) process.off(signal, handler);
      resolve(result);
    };
    child.once('error', (error) => finish({ error }));
    child.once('exit', (code, signal) =>
      finish({ code: code ?? signalExitCode(signal), signal }),
    );
  });
}

function needsSetup(code, message, remediation) {
  process.stdout.write(
    `${JSON.stringify({
      schemaVersion: 1,
      command: 'launcher',
      status: 'NEEDS_SETUP',
      error: { code, message, remediation },
    })}\n`,
  );
  return 2;
}

async function runTrustedOverride(binary, args) {
  const result = await run(binary, args, {
    cwd: process.cwd(),
    env: process.env,
    stdio: 'inherit',
  });
  if (result.error) {
    return needsSetup(
      'trusted_binary_unavailable',
      'The trusted webperf binary could not be started.',
      'Check WEBPERF_BINARY and retry.',
    );
  }
  return result.code;
}

async function buildAndExecute(args, buildDirectory) {
  const environment = controlledGoEnvironment(buildDirectory);
  await Promise.all([
    mkdir(environment.GOCACHE, { recursive: true }),
    mkdir(environment.GOMODCACHE, { recursive: true }),
    mkdir(environment.GOPATH, { recursive: true }),
    mkdir(environment.GOTMPDIR, { recursive: true }),
  ]);
  const binary = path.join(
    buildDirectory,
    process.platform === 'win32' ? 'webperf.exe' : 'webperf',
  );
  const build = await run(
    'go',
    [
      'build',
      '-trimpath',
      '-buildvcs=false',
      '-mod=readonly',
      '-o',
      binary,
      './cmd/webperf',
    ],
    {
      cwd: sourceDirectory,
      env: environment,
      stdio: 'ignore',
    },
  );
  if (build.signal) return build.code;
  if (build.error?.code === 'ENOENT') {
    return needsSetup(
      'go_required',
      'Go is required to build the bundled webperf CLI.',
      'Install Go and retry, or set WEBPERF_BINARY to a trusted executable.',
    );
  }
  if (build.error || build.code !== 0) {
    return needsSetup(
      'go_build_failed',
      'The bundled webperf CLI could not be built.',
      'Use a supported local Go toolchain or set WEBPERF_BINARY to a trusted executable.',
    );
  }

  const executed = await run(binary, args, {
    cwd: process.cwd(),
    env: process.env,
    stdio: 'inherit',
  });
  if (executed.error) {
    return needsSetup(
      'built_binary_unavailable',
      'The built webperf CLI could not be started.',
      'Retry the command or set WEBPERF_BINARY to a trusted executable.',
    );
  }
  return executed.code;
}

async function buildAndRun(args) {
  const buildDirectory = await mkdtemp(path.join(os.tmpdir(), 'webperf-build-'));
  let result;
  let operationError;
  try {
    result = await buildAndExecute(args, buildDirectory);
  } catch (error) {
    operationError = error;
  }
  try {
    await rm(buildDirectory, { recursive: true, force: true });
  } catch {
    process.stderr.write('webperf launcher: temporary build cleanup failed\n');
    if (!operationError && result === 0) result = 1;
  }
  if (operationError) throw operationError;
  return result;
}

async function main() {
  try {
    const args = process.argv.slice(2);
    const trustedBinary = process.env.WEBPERF_BINARY;
    if (trustedBinary) return await runTrustedOverride(trustedBinary, args);
    return await buildAndRun(args);
  } catch {
    return needsSetup(
      'launcher_failed',
      'The webperf launcher could not prepare the bundled CLI.',
      'Check local temporary storage and retry, or set WEBPERF_BINARY to a trusted executable.',
    );
  }
}

process.exitCode = await main();

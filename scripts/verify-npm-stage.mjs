#!/usr/bin/env node

import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { chmodSync, cpSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import process from 'node:process';

const packageName = '@baldaworks/knowl';
const stageRoot = path.resolve(process.argv[2] ?? '.omnidist/default/npm');
const expectedVersion = process.argv[3] ?? '';
const targets = [
  { platform: 'darwin', arch: 'x64', suffix: 'darwin-x64', binary: 'knowl' },
  { platform: 'darwin', arch: 'arm64', suffix: 'darwin-arm64', binary: 'knowl' },
  { platform: 'linux', arch: 'x64', suffix: 'linux-x64', binary: 'knowl' },
  { platform: 'linux', arch: 'arm64', suffix: 'linux-arm64', binary: 'knowl' },
  { platform: 'win32', arch: 'x64', suffix: 'win32-x64', binary: 'knowl.exe' },
];

const rootPackageDir = packageDirectory(stageRoot, packageName);
const rootMetadata = readJSON(path.join(rootPackageDir, 'package.json'));
assert.equal(rootMetadata.name, packageName);
assert.equal(rootMetadata.bin.knowl, 'knowl.js');
if (expectedVersion !== '') {
  assert.equal(rootMetadata.version, expectedVersion);
}

const dependencyNames = targets.map(({ suffix }) => `${packageName}-${suffix}`).sort();
assert.deepEqual(Object.keys(rootMetadata.optionalDependencies).sort(), dependencyNames);

for (const target of targets) {
  const nativeName = `${packageName}-${target.suffix}`;
  const nativeDir = packageDirectory(stageRoot, nativeName);
  const metadata = readJSON(path.join(nativeDir, 'package.json'));
  assert.equal(metadata.name, nativeName);
  assert.equal(metadata.version, rootMetadata.version);
  assert.equal(rootMetadata.optionalDependencies[nativeName], rootMetadata.version);
  assert.deepEqual(metadata.os, [target.platform]);
  assert.deepEqual(metadata.cpu, [target.arch]);

  verifySelector(rootPackageDir, nativeName, nativeDir, target);
}

await smokeCurrentPlatform(rootPackageDir);
console.log(`verified ${packageName}@${rootMetadata.version} and ${targets.length} native selectors`);

function verifySelector(sourceRootPackage, nativeName, sourceNativePackage, target) {
  const fixtureRoot = mkdtempSync(path.join(tmpdir(), 'knowl-npm-selector-'));
  const scopeDir = path.join(fixtureRoot, 'node_modules', '@baldaworks');
  const fixtureRootPackage = path.join(scopeDir, 'knowl');
  const fixtureNativePackage = path.join(scopeDir, path.basename(nativeName));
  mkdirSync(fixtureRootPackage, { recursive: true });
  mkdirSync(path.join(fixtureNativePackage, 'bin'), { recursive: true });
  cpSync(path.join(sourceRootPackage, 'knowl.js'), path.join(fixtureRootPackage, 'knowl.js'));
  cpSync(path.join(sourceRootPackage, 'package.json'), path.join(fixtureRootPackage, 'package.json'));
  cpSync(path.join(sourceNativePackage, 'package.json'), path.join(fixtureNativePackage, 'package.json'));
  writeFileSync(path.join(fixtureNativePackage, 'bin', target.binary), 'selector fixture');

  const preload = path.join(fixtureRoot, 'preload.cjs');
  const selectionResult = path.join(fixtureRoot, 'selection.json');
  writeFileSync(preload, `
const os = require('node:os');
const fs = require('node:fs');
const childProcess = require('node:child_process');
os.platform = () => process.env.KNOWL_FIXTURE_PLATFORM;
os.arch = () => process.env.KNOWL_FIXTURE_ARCH;
childProcess.execFileSync = (binary, args) => {
  fs.writeFileSync(process.env.KNOWL_FIXTURE_RESULT, JSON.stringify({ binary, args }));
  return 0;
};
`);

  const result = spawnSync(process.execPath, [
    '--require', preload,
    path.join(fixtureRootPackage, 'knowl.js'),
    'version', '--json',
  ], {
    encoding: 'utf8',
    env: {
      ...process.env,
      KNOWL_FIXTURE_PLATFORM: target.platform,
      KNOWL_FIXTURE_ARCH: target.arch,
      KNOWL_FIXTURE_RESULT: selectionResult,
    },
  });
  assert.equal(result.status, 0, result.stderr);
  const selected = readJSON(selectionResult);
  assert.equal(path.normalize(selected.binary), path.join(fixtureNativePackage, 'bin', target.binary));
  assert.deepEqual(selected.args, ['version', '--json']);
}

async function smokeCurrentPlatform(sourceRootPackage) {
  const platform = process.platform;
  const arch = process.arch;
  const target = targets.find((candidate) => candidate.platform === platform && candidate.arch === arch);
  if (target === undefined) {
    return;
  }

  const fixtureRoot = mkdtempSync(path.join(tmpdir(), 'knowl-npm-smoke-'));
  const scopeDir = path.join(fixtureRoot, 'node_modules', '@baldaworks');
  const rootDestination = path.join(scopeDir, 'knowl');
  const nativeName = `${packageName}-${target.suffix}`;
  mkdirSync(scopeDir, { recursive: true });
  cpSync(sourceRootPackage, rootDestination, { recursive: true });
  cpSync(packageDirectory(stageRoot, nativeName), path.join(scopeDir, path.basename(nativeName)), { recursive: true });

  const launcher = path.join(rootDestination, 'knowl.js');
  const isolatedEnv = {
    ...process.env,
    PATH: '',
    CODEX_HOME: path.join(fixtureRoot, 'codex-home'),
    XDG_CONFIG_HOME: path.join(fixtureRoot, 'xdg-config'),
  };
  if (expectedVersion === '') {
    await verifyStagedMCP(launcher, fixtureRoot, isolatedEnv);
    return;
  }

  const versionResult = spawnSync(process.execPath, [launcher, 'version', '--json'], {
    encoding: 'utf8',
    cwd: fixtureRoot,
    env: isolatedEnv,
  });
  assert.equal(versionResult.status, 0, versionResult.stderr);
  assert.deepEqual(JSON.parse(versionResult.stdout), {
    version: rootMetadata.version,
    tag: `v${rootMetadata.version}`,
    release: true,
  });

  verifyStagedSetup(launcher, fixtureRoot, isolatedEnv);
  await verifyStagedMCP(launcher, fixtureRoot, isolatedEnv);
}

function verifyStagedSetup(launcher, fixtureRoot, isolatedEnv) {
  const fakeBin = path.join(fixtureRoot, 'fake-bin');
  const callLog = path.join(fixtureRoot, 'codex-calls.jsonl');
  mkdirSync(fakeBin, { recursive: true });
  const expectedCalls = [
    ['plugin', 'marketplace', 'list', '--json'],
    ['plugin', 'marketplace', 'add', 'baldaworks/knowl', '--ref', `v${rootMetadata.version}`, '--sparse', '.agents/plugins', '--sparse', 'plugins/knowl', '--json'],
    ['plugin', 'list', '--marketplace', 'knowl', '--available', '--json'],
    ['plugin', 'add', 'knowl@knowl', '--json'],
  ];
  const responses = [
    { marketplaces: [] },
    { marketplaceName: 'knowl', alreadyAdded: false },
    { installed: [], available: [{ pluginId: 'knowl@knowl', name: 'knowl', marketplaceName: 'knowl', version: rootMetadata.version, installed: false, enabled: true }] },
    { pluginId: 'knowl@knowl', marketplaceName: 'knowl', version: rootMetadata.version },
  ];
  const fakeCodex = path.join(fakeBin, process.platform === 'win32' ? 'codex.js' : 'codex');
  writeFileSync(fakeCodex, `#!${process.execPath}\n` +
    `const fs = require('node:fs');\n` +
    `const calls = ${JSON.stringify(expectedCalls)};\n` +
    `const responses = ${JSON.stringify(responses)};\n` +
    `const args = process.argv.slice(2);\n` +
    `fs.appendFileSync(process.env.KNOWL_CODEX_CALL_LOG, JSON.stringify(args) + '\\n');\n` +
    `const index = calls.findIndex((call) => JSON.stringify(call) === JSON.stringify(args));\n` +
    `if (index < 0) process.exit(97);\n` +
    `process.stdout.write(JSON.stringify(responses[index]));\n`);
  chmodSync(fakeCodex, 0o700);
  if (process.platform === 'win32') {
    writeFileSync(path.join(fakeBin, 'codex.cmd'), `@"${process.execPath}" "${fakeCodex}" %*\r\n`);
  }

  const setupResult = spawnSync(process.execPath, [launcher, 'setup', 'codex', '--skip-project'], {
    encoding: 'utf8',
    cwd: fixtureRoot,
    env: { ...isolatedEnv, PATH: fakeBin, KNOWL_CODEX_CALL_LOG: callLog },
  });
  assert.equal(setupResult.status, 0, setupResult.stderr);
  assert.deepEqual(JSON.parse(setupResult.stdout), {
    version: rootMetadata.version,
    project: 'skipped',
    marketplace: 'added',
    plugin: 'installed',
    restart_required: true,
  });
  const calls = readFileSync(callLog, 'utf8').trim().split('\n').map((line) => JSON.parse(line));
  assert.deepEqual(calls, expectedCalls);
}

async function verifyStagedMCP(launcher, fixtureRoot, isolatedEnv) {
  const project = path.join(fixtureRoot, 'mcp-project');
  mkdirSync(project, { recursive: true });
  const initResult = spawnSync(process.execPath, [launcher, 'init'], {
    encoding: 'utf8',
    cwd: project,
    env: isolatedEnv,
  });
  assert.equal(initResult.status, 0, initResult.stderr);

  const child = spawn(process.execPath, [launcher, 'mcp', 'stdio'], {
    cwd: project,
    env: isolatedEnv,
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const pending = new Map();
  const frames = [];
  let stdoutBuffer = '';
  let stderr = '';
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (chunk) => { stderr += chunk; });
  child.stdout.setEncoding('utf8');
  child.stdout.on('data', (chunk) => {
    stdoutBuffer += chunk;
    for (;;) {
      const newline = stdoutBuffer.indexOf('\n');
      if (newline < 0) break;
      const line = stdoutBuffer.slice(0, newline);
      stdoutBuffer = stdoutBuffer.slice(newline + 1);
      if (line === '') continue;
      const frame = JSON.parse(line);
      assert.equal(frame.jsonrpc, '2.0');
      frames.push(frame);
      const resolve = pending.get(frame.id);
      if (resolve !== undefined) {
        pending.delete(frame.id);
        resolve(frame);
      }
    }
  });

  let nextID = 1;
  const request = (method, params = {}) => {
    const id = nextID++;
    const response = withTimeout(new Promise((resolve) => pending.set(id, resolve)), `MCP ${method}`);
    child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`);
    return response;
  };

  try {
    const initialized = await request('initialize', {
      protocolVersion: '2025-06-18',
      capabilities: {},
      clientInfo: { name: 'knowl-npm-stage', version: rootMetadata.version },
    });
    assert.equal(initialized.error, undefined);
    assert.equal(initialized.result.serverInfo.name, 'knowl');
    assert.equal(typeof initialized.result.protocolVersion, 'string');
    child.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized', params: {} })}\n`);

    const listed = await request('tools/list');
    assert.equal(listed.error, undefined);
    const tools = listed.result.tools.map((tool) => ({
      name: tool.name,
      readOnly: tool.annotations?.readOnlyHint ?? false,
    })).sort((left, right) => left.name.localeCompare(right.name));
    assert.deepEqual(tools, [
      { name: 'knowl_ingest', readOnly: false },
      { name: 'knowl_operation', readOnly: true },
      { name: 'knowl_retrieve', readOnly: true },
    ]);

    child.stdin.end();
    const [code, signal] = await withTimeout(new Promise((resolve) => child.once('close', (...args) => resolve(args))), 'MCP shutdown');
    assert.equal(code, 0, stderr);
    assert.equal(signal, null);
    assert.equal(stdoutBuffer, '');
    assert.equal(stderr, '');
    assert.ok(frames.length >= 2);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
  }
}

function withTimeout(promise, label) {
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${label} timed out`)), 15000);
  });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function packageDirectory(root, scopedName) {
  return path.join(root, ...scopedName.split('/'));
}

function readJSON(file) {
  return JSON.parse(readFileSync(file, 'utf8'));
}

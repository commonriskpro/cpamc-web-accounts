#!/usr/bin/env node
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {spawn, spawnSync} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import http from 'node:http';

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const BUILD = path.join(ROOT, 'build.mjs');

function readVarint(buffer, offset) {
  let value = 0n;
  let shift = 0n;
  let position = offset;
  while (position < buffer.length) {
    const byte = buffer[position++];
    value |= BigInt(byte & 0x7f) << shift;
    if ((byte & 0x80) === 0) return {value, offset: position};
    shift += 7n;
    if (shift > 70n) throw new Error('varint too long');
  }
  throw new Error('truncated varint');
}

function parseFields(buffer) {
  const fields = new Map();
  let offset = 0;
  while (offset < buffer.length) {
    const tag = readVarint(buffer, offset); offset = tag.offset;
    const field = Number(tag.value >> 3n);
    const wire = Number(tag.value & 7n);
    if (wire === 2) {
      const length = readVarint(buffer, offset); offset = length.offset;
      const end = offset + Number(length.value);
      if (end > buffer.length) throw new Error('truncated bytes field');
      fields.set(field, buffer.subarray(offset, end)); offset = end;
    } else if (wire === 0) {
      const value = readVarint(buffer, offset); offset = value.offset;
      fields.set(field, value.value);
    } else throw new Error(`unsupported wire type ${wire}`);
  }
  return fields;
}

function extensionId(publicKeyDer) {
  const hex = crypto.createHash('sha256').update(publicKeyDer).digest('hex').slice(0, 32);
  return [...hex].map((digit) => String.fromCharCode('a'.charCodeAt(0) + Number.parseInt(digit, 16))).join('');
}

function verifyCrx(crx) {
  assert.equal(crx.subarray(0, 4).toString('ascii'), 'Cr24');
  assert.equal(crx.readUInt32LE(4), 3);
  const headerSize = crx.readUInt32LE(8);
  const header = crx.subarray(12, 12 + headerSize);
  const archive = crx.subarray(12 + headerSize);
  assert.equal(archive.subarray(0, 2).toString('ascii'), 'PK');
  const headerFields = parseFields(header);
  const proof = parseFields(headerFields.get(2));
  const signedHeaderData = headerFields.get(10000);
  assert.ok(proof.get(1));
  assert.ok(proof.get(2));
  assert.ok(signedHeaderData);
  const signedData = parseFields(signedHeaderData);
  const expectedCrxId = crypto.createHash('sha256').update(proof.get(1)).digest().subarray(0, 16);
  assert.deepEqual(Buffer.from(signedData.get(1)), expectedCrxId);
  const size = Buffer.alloc(4); size.writeUInt32LE(signedHeaderData.length);
  const signedPayload = Buffer.concat([Buffer.from('CRX3 SignedData\0'), size, signedHeaderData, archive]);
  const publicKey = crypto.createPublicKey({key: proof.get(1), format: 'der', type: 'spki'});
  assert.equal(crypto.verify('sha256', signedPayload, {key: publicKey, padding: crypto.constants.RSA_PKCS1_PADDING}, proof.get(2)), true);
  return {publicKeyDer: proof.get(1), crxId: expectedCrxId, archive};
}

function request(url, method = 'GET') {
  return new Promise((resolve, reject) => {
    const req = http.request(url, {method}, (response) => {
      const chunks = [];
      response.on('data', (chunk) => chunks.push(chunk));
      response.on('end', () => resolve({status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks)}));
    });
    req.on('error', reject);
    req.end();
  });
}

function parsePlist(file) {
  const plutil = spawnSync('plutil', ['-convert', 'json', '-o', '-', file], {encoding: 'utf8'});
  if (!plutil.error && plutil.status === 0) return JSON.parse(plutil.stdout);
  const python = spawnSync('python3', ['-c', 'import json, plistlib, sys; json.dump(plistlib.load(sys.stdin.buffer), sys.stdout)'], {
    input: fs.readFileSync(file), encoding: 'utf8',
  });
  if (!python.error && python.status === 0) return JSON.parse(python.stdout);
  throw new Error(`no plist parser available: ${plutil.stderr || python.stderr || 'plutil/python3 failed'}`);
}

function assertProfileSemantics(file, metadata) {
  const profile = parsePlist(file);
  assert.equal(profile.PayloadType, 'Configuration');
  assert.equal(profile.PayloadScope, 'User');
  assert.ok(Array.isArray(profile.PayloadContent));
  assert.equal(profile.PayloadContent.length, 1);
  const chrome = profile.PayloadContent[0];
  assert.equal(chrome.PayloadType, 'com.google.Chrome');
  assert.equal(chrome.PayloadScope, 'User');
  assert.deepEqual(chrome.ExtensionInstallSources, [metadata.sourcePattern]);
  assert.deepEqual(chrome.ExtensionSettings, {
    [metadata.extensionId]: {installation_mode: 'allowed'},
  });
  const policyKeys = Object.keys(chrome).filter((key) => ['ExtensionInstallSources', 'ExtensionSettings'].includes(key)).sort();
  assert.deepEqual(policyKeys, ['ExtensionInstallSources', 'ExtensionSettings']);
  assert.equal(Object.hasOwn(chrome, 'ExtensionInstallForcelist'), false);
  assert.equal(Object.hasOwn(chrome, 'ExtensionInstallAllowlist'), false);
}

async function waitForReady(child) {
  return new Promise((resolve, reject) => {
    let output = '';
    const onData = (chunk) => {
      output += chunk.toString();
      const match = output.match(/READY (http:\/\/127\.0\.0\.1:\d+\/cpamc-install\/[a-f0-9]{36}\/index\.html)/);
      if (match) { child.stdout.off('data', onData); resolve(match[1]); }
    };
    child.stdout.on('data', onData);
    child.once('error', reject);
    child.once('exit', (code) => reject(new Error(`server exited before ready (${code}): ${output}`)));
    setTimeout(() => reject(new Error(`server did not become ready: ${output}`)), 5000).unref();
  });
}

async function main() {
  const output = fs.mkdtempSync(path.join(os.tmpdir(), 'cpamc-installation-probe-'));
  try {
    const build = spawnSync(process.execPath, [BUILD, '--build', '--output', output], {encoding: 'utf8'});
    assert.equal(build.status, 0, build.stderr);
    const metadata = JSON.parse(fs.readFileSync(path.join(output, 'metadata.json'), 'utf8'));
    assert.match(metadata.extensionId, /^[a-p]{32}$/);
    assert.match(metadata.token, /^[a-f0-9]{36}$/);
    assert.equal(metadata.sourcePattern, `http://127.0.0.1/cpamc-install/${metadata.token}/*`);
    const manifest = JSON.parse(fs.readFileSync(path.join(output, 'manifest.json'), 'utf8'));
    assert.equal(manifest.name, 'CPAMC Installation Probe');
    assert.equal(manifest.version, '0.0.1');
    assert.deepEqual(Object.keys(manifest).sort(), ['description', 'key', 'manifest_version', 'name', 'version']);
    const crx = fs.readFileSync(path.join(output, 'probe.crx'));
    const verified = verifyCrx(crx);
    assert.equal(manifest.key, verified.publicKeyDer.toString('base64'));
    assert.equal(metadata.extensionId, extensionId(verified.publicKeyDer));
    const archivePath = path.join(output, '.test-archive.zip'); fs.writeFileSync(archivePath, verified.archive);
    const list = spawnSync('unzip', ['-Z1', archivePath], {encoding: 'utf8'});
    assert.equal(list.status, 0, list.stderr);
    assert.deepEqual(list.stdout.trim().split(/\r?\n/), ['manifest.json']);
    const archiveManifest = spawnSync('unzip', ['-p', archivePath, 'manifest.json'], {encoding: 'utf8'});
    assert.equal(archiveManifest.status, 0, archiveManifest.stderr);
    assert.deepEqual(JSON.parse(archiveManifest.stdout), manifest);
    fs.rmSync(archivePath, {force: true});

    const profilePath = path.join(output, 'chrome-installation-probe.mobileconfig');
    const profile = fs.readFileSync(profilePath, 'utf8');
    assert.match(profile, new RegExp(`<key>${metadata.extensionId}</key>`));
    assert.ok(profile.includes(`<string>${metadata.sourcePattern}</string>`));
    assert.doesNotMatch(profile, /force_installed|ExtensionInstallForcelist|ExtensionInstallAllowlist|\*:\/\/\*\/\*/);
    assertProfileSemantics(profilePath, metadata);
    const plutil = spawnSync('plutil', ['-lint', profilePath], {encoding: 'utf8'});
    if (plutil.error?.code !== 'ENOENT') assert.equal(plutil.status, 0, plutil.stderr || plutil.stdout);

    const server = spawn(process.execPath, [BUILD, '--serve', '--output', output, '--port', '0'], {cwd: ROOT, stdio: ['ignore', 'pipe', 'pipe']});
    try {
      const indexUrl = await waitForReady(server);
      const index = await request(indexUrl);
      assert.equal(index.status, 200);
      assert.match(index.body.toString(), /Instalar extensión de prueba/);
      assert.doesNotMatch(index.body.toString(), /window\.location|setTimeout|fetch\(/);
      const crxUrl = new URL(indexUrl); crxUrl.pathname = `/cpamc-install/${metadata.token}/probe.crx`;
      const servedCrx = await request(crxUrl);
      assert.equal(servedCrx.status, 200);
      assert.equal(servedCrx.headers['content-type'], 'application/x-chrome-extension');
      assert.equal(servedCrx.headers['content-disposition'], 'attachment; filename="probe.crx"');
      assert.deepEqual(servedCrx.body, crx);
      const badFinalNibble = metadata.token.endsWith('0') ? '1' : '0';
      const badUrl = indexUrl.replace(metadata.token, `${metadata.token.slice(0, -1)}${badFinalNibble}`);
      assert.notEqual(badUrl, indexUrl);
      const badToken = await request(badUrl);
      assert.equal(badToken.status, 404);
      const query = await request(`${indexUrl}?unexpected=1`);
      assert.equal(query.status, 404);
      const traversal = await request(new URL(`/cpamc-install/${metadata.token}/../probe.crx`, indexUrl));
      assert.equal(traversal.status, 404);
      const post = await request(indexUrl, 'POST');
      assert.equal(post.status, 405);
    } finally {
      server.kill('SIGTERM');
      await new Promise((resolve) => server.once('exit', resolve));
    }
    console.log('PASS installation probe: CRX3 signature, manifest allowlist, policy XML, and server route checks');
  } finally {
    fs.rmSync(output, {recursive: true, force: true});
  }
}

main().catch((error) => { console.error(`FAIL ${error.stack || error}`); process.exitCode = 1; });

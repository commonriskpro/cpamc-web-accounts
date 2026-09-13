#!/usr/bin/env node
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import {spawnSync} from 'node:child_process';
import http from 'node:http';
import {fileURLToPath} from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)));
const DEFAULT_OUTPUT = path.resolve(ROOT, '..', 'artifacts', 'installation-probe');
const EXTENSION_VERSION = '0.0.1';
const EXTENSION_NAME = 'CPAMC Installation Probe';
const SIGNATURE_CONTEXT = Buffer.from('CRX3 SignedData\0', 'utf8');

function fail(message) {
  throw new Error(message);
}

function parseArgs(argv) {
  const args = [...argv];
  const mode = args[0] === '--serve' ? 'serve' : 'build';
  if (mode === 'serve') args.shift();
  else if (args[0] === '--build') args.shift();
  let output = DEFAULT_OUTPUT;
  let port = 0;
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === '--output') output = path.resolve(args[++i] ?? fail('--output needs a directory'));
    else if (arg === '--port') port = Number(args[++i] ?? fail('--port needs a number'));
    else fail(`unknown argument: ${arg}`);
  }
  if (!Number.isInteger(port) || port < 0 || port > 65535) fail(`invalid port: ${port}`);
  return {mode, output, port};
}

function encodeVarint(value) {
  let n = BigInt(value);
  if (n < 0n) fail('protobuf varints cannot be negative');
  const bytes = [];
  do {
    let byte = Number(n & 0x7fn);
    n >>= 7n;
    if (n !== 0n) byte |= 0x80;
    bytes.push(byte);
  } while (n !== 0n);
  return Buffer.from(bytes);
}

function fieldBytes(fieldNumber, value) {
  const data = Buffer.isBuffer(value) ? value : Buffer.from(value);
  const tag = (BigInt(fieldNumber) << 3n) | 2n;
  return Buffer.concat([encodeVarint(tag), encodeVarint(data.length), data]);
}

function protobuf(fields) {
  return Buffer.concat(fields.map(([number, value]) => fieldBytes(number, value)));
}

function xmlEscape(value) {
  return String(value).replace(/[&<>"']/g, (character) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;',
  }[character]));
}

function writePrivateKey(file, key) {
  fs.writeFileSync(file, key, {encoding: 'utf8', mode: 0o600});
  fs.chmodSync(file, 0o600);
}

function extensionId(publicKeyDer) {
  const hex = crypto.createHash('sha256').update(publicKeyDer).digest('hex').slice(0, 32);
  return [...hex].map((digit) => String.fromCharCode('a'.charCodeAt(0) + Number.parseInt(digit, 16))).join('');
}

function makeManifest(publicKeyBase64) {
  return `${JSON.stringify({
    manifest_version: 3,
    name: EXTENSION_NAME,
    version: EXTENSION_VERSION,
    description: 'Inert, permission-free installation probe for CPAMC. It has no runtime behavior.',
    key: publicKeyBase64,
  }, null, 2)}\n`;
}

function makeProfile(extensionIdValue, sourcePattern) {
  const uuid = crypto.randomUUID().toUpperCase();
  const rootUuid = crypto.randomUUID().toUpperCase();
  const source = xmlEscape(sourcePattern);
  const id = xmlEscape(extensionIdValue);
  return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>PayloadType</key><string>Configuration</string>
  <key>PayloadContent</key>
  <array>
    <dict>
      <key>PayloadType</key><string>com.google.Chrome</string>
      <key>PayloadIdentifier</key><string>com.cpamc.installation-probe.chrome</string>
      <key>PayloadUUID</key><string>${uuid}</string>
      <key>PayloadVersion</key><integer>1</integer>
      <key>PayloadDisplayName</key><string>CPAMC installation probe (experimental)</string>
      <key>PayloadDescription</key><string>Allows this exact extension ID to be offered from the local CPAMC probe URL. It does not force installation.</string>
      <key>PayloadScope</key><string>User</string>
      <key>ExtensionInstallSources</key>
      <array><string>${source}</string></array>
      <key>ExtensionSettings</key>
      <dict>
        <key>${id}</key>
        <dict><key>installation_mode</key><string>allowed</string></dict>
      </dict>
    </dict>
  </array>
  <key>PayloadDisplayName</key><string>CPAMC Chrome installation probe</string>
  <key>PayloadIdentifier</key><string>com.cpamc.installation-probe</string>
  <key>PayloadOrganization</key><string>CPAMC (experimental)</string>
  <key>PayloadDescription</key><string>Optional, user-approved Chrome policy for testing a permission-free CPAMC installation flow.</string>
  <key>PayloadUUID</key><string>${rootUuid}</string>
  <key>PayloadVersion</key><integer>1</integer>
  <key>PayloadScope</key><string>User</string>
</dict>
</plist>
`;
}

function makePage(token) {
  const href = `/cpamc-install/${encodeURIComponent(token)}/probe.crx`;
  return `<!doctype html>
<html lang="es">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>CPAMC installation probe</title></head>
<body>
  <main>
    <h1>Prueba de instalación de CPAMC para Chrome</h1>
    <p>Es una extensión experimental sin permisos. No tiene scripts de fondo, scripts de contenido ni acceso a cuentas.</p>
    <p>Después de instalar manualmente el perfil local de Chrome, pulsa el botón. Chrome puede mostrar su confirmación normal.</p>
    <p><a href="${href}">Instalar extensión de prueba</a></p>
    <p>Si Chrome descarga el archivo en lugar de mostrar una confirmación, no se instala automáticamente: revisa el resultado antes de continuar.</p>
  </main>
</body>
</html>
`;
}

function zipManifest(manifest, output) {
  const tempDir = fs.mkdtempSync(path.join(output, '.build-'));
  try {
    fs.writeFileSync(path.join(tempDir, 'manifest.json'), manifest, {mode: 0o600});
    const zip = path.join(tempDir, 'extension.zip');
    const result = spawnSync('zip', ['-q', '-X', zip, 'manifest.json'], {cwd: tempDir, encoding: 'utf8'});
    if (result.error) fail(`zip unavailable: ${result.error.message}`);
    if (result.status !== 0) fail(`zip failed: ${result.stderr || result.stdout || result.status}`);
    return fs.readFileSync(zip);
  } finally {
    fs.rmSync(tempDir, {recursive: true, force: true});
  }
}

function createCrx3(privateKeyPem, publicKeyDer, archive) {
  const key = crypto.createPrivateKey(privateKeyPem);
  const crxId = crypto.createHash('sha256').update(publicKeyDer).digest().subarray(0, 16);
  const signedHeaderData = protobuf([[1, crxId]]);
  const signedPayload = Buffer.concat([
    SIGNATURE_CONTEXT,
    (() => { const b = Buffer.alloc(4); b.writeUInt32LE(signedHeaderData.length); return b; })(),
    signedHeaderData,
    archive,
  ]);
  const signature = crypto.sign('sha256', signedPayload, {key, padding: crypto.constants.RSA_PKCS1_PADDING});
  const proof = protobuf([[1, publicKeyDer], [2, signature]]);
  const header = protobuf([[2, proof], [10000, signedHeaderData]]);
  const prefix = Buffer.alloc(12);
  prefix.write('Cr24', 0, 4, 'ascii');
  prefix.writeUInt32LE(3, 4);
  prefix.writeUInt32LE(header.length, 8);
  return Buffer.concat([prefix, header, archive]);
}

function build(output) {
  fs.mkdirSync(output, {recursive: true, mode: 0o700});
  fs.chmodSync(output, 0o700);
  const privateKeyPath = path.join(output, 'private-key.pem');
  let privateKeyPem;
  if (fs.existsSync(privateKeyPath)) {
    privateKeyPem = fs.readFileSync(privateKeyPath, 'utf8');
    try { crypto.createPrivateKey(privateKeyPem); } catch (error) { fail(`invalid existing signing key: ${error.message}`); }
  } else {
    const {privateKey} = crypto.generateKeyPairSync('rsa', {modulusLength: 2048, publicExponent: 0x10001});
    privateKeyPem = privateKey.export({type: 'pkcs8', format: 'pem'});
    writePrivateKey(privateKeyPath, privateKeyPem);
  }
  fs.chmodSync(privateKeyPath, 0o600);
  const publicKeyDer = crypto.createPublicKey(privateKeyPem).export({type: 'spki', format: 'der'});
  const publicKeyBase64 = publicKeyDer.toString('base64');
  const id = extensionId(publicKeyDer);
  const token = crypto.randomBytes(18).toString('hex');
  const sourcePattern = `http://127.0.0.1/cpamc-install/${token}/*`;
  const manifest = makeManifest(publicKeyBase64);
  const archive = zipManifest(manifest, output);
  const crx = createCrx3(privateKeyPem, publicKeyDer, archive);
  const paths = {
    outputDir: path.resolve(output),
    crx: path.join(path.resolve(output), 'probe.crx'),
    manifest: path.join(path.resolve(output), 'manifest.json'),
    mobileconfig: path.join(path.resolve(output), 'chrome-installation-probe.mobileconfig'),
    page: path.join(path.resolve(output), 'index.html'),
    privateKey: path.join(path.resolve(output), 'private-key.pem'),
  };
  fs.writeFileSync(paths.crx, crx, {mode: 0o600});
  fs.writeFileSync(paths.manifest, manifest, {mode: 0o600});
  fs.writeFileSync(paths.mobileconfig, makeProfile(id, sourcePattern), {mode: 0o600});
  fs.writeFileSync(paths.page, makePage(token), {mode: 0o600});
  const metadata = {
    format: 'cpamc-installation-probe/v1',
    extensionName: EXTENSION_NAME,
    extensionVersion: EXTENSION_VERSION,
    extensionId: id,
    token,
    sourcePattern,
    generatedAt: new Date().toISOString(),
    paths,
    server: {host: '127.0.0.1', routePrefix: `/cpamc-install/${token}/`},
    policy: {scope: 'User', installationMode: 'allowed', forceInstall: false},
  };
  fs.writeFileSync(path.join(output, 'metadata.json'), `${JSON.stringify(metadata, null, 2)}\n`, {mode: 0o600});
  return metadata;
}

function loadMetadata(output) {
  const metadataPath = path.join(output, 'metadata.json');
  if (!fs.existsSync(metadataPath)) fail(`missing generated metadata: ${metadataPath}; run build first`);
  const metadata = JSON.parse(fs.readFileSync(metadataPath, 'utf8'));
  if (!/^[a-f0-9]{36}$/.test(metadata.token) || !/^[a-p]{32}$/.test(metadata.extensionId)) fail('invalid generated metadata');
  for (const file of ['probe.crx', 'index.html']) if (!fs.existsSync(path.join(output, file))) fail(`missing generated file: ${file}`);
  return metadata;
}

function serve(output, port) {
  const metadata = loadMetadata(output);
  const tokenPrefix = `/cpamc-install/${metadata.token}/`;
  const page = fs.readFileSync(path.join(output, 'index.html'));
  const crx = fs.readFileSync(path.join(output, 'probe.crx'));
  const server = http.createServer((request, response) => {
    if (request.method !== 'GET') {
      response.writeHead(405, {'Content-Type': 'text/plain; charset=utf-8', Allow: 'GET'});
      response.end('method not allowed');
      return;
    }
    const url = new URL(request.url ?? '/', 'http://127.0.0.1');
    if (!url.search && url.pathname === `${tokenPrefix}index.html`) {
      response.writeHead(200, {'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store'});
      response.end(page);
    } else if (!url.search && url.pathname === `${tokenPrefix}probe.crx`) {
      response.writeHead(200, {
        'Content-Type': 'application/x-chrome-extension',
        'Content-Disposition': 'attachment; filename="probe.crx"',
        'Cache-Control': 'no-store',
      });
      response.end(crx);
    } else {
      response.writeHead(404, {'Content-Type': 'text/plain; charset=utf-8'});
      response.end('not found');
    }
  });
  server.on('error', (error) => { console.error(`server error: ${error.message}`); process.exitCode = 1; });
  server.listen(port, '127.0.0.1', () => {
    const address = server.address();
    console.log(`READY http://127.0.0.1:${address.port}${tokenPrefix}index.html`);
  });
  const close = () => server.close(() => process.exit(0));
  process.on('SIGINT', close);
  process.on('SIGTERM', close);
  return server;
}

try {
  const {mode, output, port} = parseArgs(process.argv.slice(2));
  if (mode === 'serve') serve(output, port);
  else {
    const metadata = build(output);
    console.log(`BUILT ${metadata.extensionId} ${metadata.sourcePattern}`);
    console.log(`OUTPUT ${metadata.paths.outputDir}`);
  }
} catch (error) {
  console.error(`ERROR ${error.message}`);
  process.exitCode = 1;
}

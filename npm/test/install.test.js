'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { EventEmitter } = require('node:events');
const { PassThrough } = require('node:stream');
const test = require('node:test');
const { download, install } = require('../install');
const PACKAGE_VERSION = require('../package.json').version;

function fakeGet(routes) {
  return (url, _options, callback) => {
    const request = new EventEmitter();
    request.setTimeout = () => request;
    request.destroy = (error) => {
      if (error) queueMicrotask(() => request.emit('error', error));
    };
    queueMicrotask(() => {
      const route = routes[url];
      if (!route || route.never) return;
      if (route.error) {
        request.emit('error', route.error);
        return;
      }
      const response = new PassThrough();
      response.statusCode = route.status || 200;
      response.headers = route.headers || {};
      callback(response);
      response.end(route.body || Buffer.alloc(0));
    });
    return request;
  };
}

function fixture(t, changes = {}) {
  const packageDir = fs.mkdtempSync(path.join(os.tmpdir(), 'bp-npm-install-'));
  t.after(() => fs.rmSync(packageDir, { recursive: true, force: true }));
  fs.mkdirSync(path.join(packageDir, 'bin'));
  fs.writeFileSync(path.join(packageDir, 'package.json'), JSON.stringify({ version: PACKAGE_VERSION }));
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  fs.writeFileSync(path.join(packageDir, 'release.pub'), publicKey.export({ type: 'spki', format: 'pem' }));
  const binary = changes.binary || Buffer.from('native executable');
  const hash = changes.hash || crypto.createHash('sha256').update(binary).digest('hex');
  const manifestBytes = Buffer.from(JSON.stringify({
    version: changes.manifestVersion || PACKAGE_VERSION,
    revision: 'fixture',
    sha256: { 'bp-linux-amd64': hash }
  }));
  const signature = crypto.sign(null, manifestBytes, privateKey);
  if (changes.corruptSignature) signature[0] ^= 0xff;
  const fetch = async (url) => {
    if (url.endsWith('/manifest.json')) return manifestBytes;
    if (url.endsWith('/manifest.sig')) return signature;
    if (url.endsWith('/bp-linux-amd64')) return binary;
    throw new Error(`unexpected URL ${url}`);
  };
  return { binary, fetch, packageDir };
}

test('signed install writes the verified platform binary executable', async (t) => {
  const value = fixture(t);
  const result = await install({
    packageDir: value.packageDir,
    platform: 'linux',
    architecture: 'x64',
    download: value.fetch
  });
  assert.equal(result.filename, 'bp-linux-amd64');
  assert.deepEqual(fs.readFileSync(result.target), value.binary);
  assert.equal(fs.statSync(result.target).mode & 0o777, 0o755);
});

test('install rejects a corrupt signature before writing a binary', async (t) => {
  const value = fixture(t, { corruptSignature: true });
  await assert.rejects(
    install({ packageDir: value.packageDir, platform: 'linux', architecture: 'x64', download: value.fetch }),
    /release signature failed/
  );
  assert.equal(fs.existsSync(path.join(value.packageDir, 'bin', 'bp-linux-amd64')), false);
});

test('install rejects signed manifests with the wrong version', async (t) => {
  const value = fixture(t, { manifestVersion: '9.9.9' });
  await assert.rejects(
    install({ packageDir: value.packageDir, platform: 'linux', architecture: 'x64', download: value.fetch }),
    /release version mismatch/
  );
});

test('install rejects a binary that does not match the signed hash', async (t) => {
  const value = fixture(t, { hash: '0'.repeat(64) });
  await assert.rejects(
    install({ packageDir: value.packageDir, platform: 'linux', architecture: 'x64', download: value.fetch }),
    /SHA-256 mismatch/
  );
});

test('download follows HTTPS redirects and rejects HTTP failures', async () => {
  const get = fakeGet({
    'https://release.test/start': { status: 302, headers: { location: '/final' } },
    'https://release.test/final': { body: Buffer.from('payload') },
    'https://release.test/failure': { status: 503 }
  });
  assert.equal((await download('https://release.test/start', { get })).toString(), 'payload');
  await assert.rejects(download('https://release.test/failure', { get }), /HTTP 503/);
});

test('download refuses non-HTTPS redirects and enforces an absolute timeout', async () => {
  const redirect = fakeGet({
    'https://release.test/start': { status: 302, headers: { location: 'http://release.test/final' } }
  });
  await assert.rejects(download('https://release.test/start', { get: redirect }), /non-HTTPS redirect/);
  const stalled = fakeGet({ 'https://release.test/stalled': { never: true } });
  await assert.rejects(download('https://release.test/stalled', { get: stalled, timeoutMs: 20 }), /timed out/);
});

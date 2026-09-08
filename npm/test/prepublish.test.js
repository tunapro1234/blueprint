'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { ARTIFACTS, verifyPublishedRelease } = require('../scripts/prepublish');
const PACKAGE_VERSION = require('../package.json').version;

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'bp-npm-publish-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const packageDir = path.join(root, 'npm');
  fs.mkdirSync(packageDir);
  fs.writeFileSync(path.join(packageDir, 'package.json'), JSON.stringify({ version: PACKAGE_VERSION }));
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  fs.writeFileSync(path.join(packageDir, 'release.pub'), publicKey.export({ type: 'spki', format: 'pem' }));
  const binaries = Object.fromEntries(ARTIFACTS.map((name) => [name, Buffer.from(`fixture ${name}`)]));
  const manifestBytes = Buffer.from(JSON.stringify({
    version: PACKAGE_VERSION,
    revision: 'fixture-revision',
    sha256: Object.fromEntries(ARTIFACTS.map((name) => [
      name, crypto.createHash('sha256').update(binaries[name]).digest('hex')
    ]))
  }));
  const signature = crypto.sign(null, manifestBytes, privateKey);
  const requested = [];
  const download = async (url) => {
    requested.push(url);
    if (url.endsWith('/manifest.json')) return manifestBytes;
    if (url.endsWith('/manifest.sig')) return signature;
    const name = path.basename(new URL(url).pathname);
    if (binaries[name]) return binaries[name];
    throw new Error(`unexpected URL ${url}`);
  };
  return { binaries, download, packageDir, requested };
}

test('publish gate verifies every supported native artifact', async (t) => {
  const value = fixture(t);
  const result = await verifyPublishedRelease({
    packageDir: value.packageDir,
    download: value.download,
    skipPackCheck: true
  });
  assert.deepEqual(result, { version: PACKAGE_VERSION, revision: 'fixture-revision' });
  for (const name of ARTIFACTS) assert.equal(value.requested.some((url) => url.endsWith(`/${name}`)), true);
});

test('publish gate blocks one corrupt platform artifact', async (t) => {
  const value = fixture(t);
  value.binaries['bp-darwin-arm64'] = Buffer.from('tampered');
  await assert.rejects(verifyPublishedRelease({
    packageDir: value.packageDir,
    download: value.download,
    skipPackCheck: true
  }), /SHA-256 mismatch for bp-darwin-arm64/);
});

test('publish gate blocks checkout version and key drift', async (t) => {
  const value = fixture(t);
  const releaseDir = path.join(path.dirname(value.packageDir), 'internal', 'release');
  fs.mkdirSync(releaseDir, { recursive: true });
  fs.writeFileSync(path.join(releaseDir, 'version.txt'), '0.0.0\n');
  fs.copyFileSync(path.join(value.packageDir, 'release.pub'), path.join(releaseDir, 'release.pub'));
  await assert.rejects(verifyPublishedRelease({
    packageDir: value.packageDir,
    download: value.download,
    skipPackCheck: true
  }), /npm\/native version mismatch/);

  fs.writeFileSync(path.join(releaseDir, 'version.txt'), `${PACKAGE_VERSION}\n`);
  const other = crypto.generateKeyPairSync('ed25519').publicKey.export({ type: 'spki', format: 'pem' });
  fs.writeFileSync(path.join(releaseDir, 'release.pub'), other);
  await assert.rejects(verifyPublishedRelease({
    packageDir: value.packageDir,
    download: value.download,
    skipPackCheck: true
  }), /npm\/native public key mismatch/);
});

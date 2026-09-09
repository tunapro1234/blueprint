'use strict';

const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const childProcess = require('node:child_process');
const test = require('node:test');
const { PACKED_FILES } = require('../scripts/prepublish');
const { platformFilename } = require('../bin/bp');

const PACKAGE_ROOT = path.resolve(__dirname, '..');
const PACKAGE_VERSION = require('../package.json').version;

test('actual tarball runs signed postinstall and uninstalls from a private prefix', { skip: !platformFilename() }, (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'bp-npm-package-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const source = path.join(directory, 'source');
  const packDirectory = path.join(directory, 'pack');
  const prefix = path.join(directory, 'prefix');
  fs.mkdirSync(source);
  fs.mkdirSync(packDirectory);
  fs.mkdirSync(path.join(source, 'bin'));
  for (const name of ['LICENSE', 'README.md', 'install.js', 'package.json']) {
    fs.copyFileSync(path.join(PACKAGE_ROOT, name), path.join(source, name));
  }
  fs.copyFileSync(path.join(PACKAGE_ROOT, 'bin', 'bp.js'), path.join(source, 'bin', 'bp.js'));

  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  fs.writeFileSync(path.join(source, 'release.pub'), publicKey.export({ type: 'spki', format: 'pem' }));
  const releaseDir = path.join(directory, 'release');
  fs.mkdirSync(releaseDir);
  const filename = platformFilename();
  const binary = Buffer.from(`#!/usr/bin/env node\nconsole.log('fixture native ' + process.argv.slice(2).join('|'));\n`);
  fs.writeFileSync(path.join(releaseDir, filename), binary);
  const manifestBytes = Buffer.from(JSON.stringify({
    version: PACKAGE_VERSION,
    revision: 'fixture-revision',
    sha256: { [filename]: crypto.createHash('sha256').update(binary).digest('hex') }
  }));
  fs.writeFileSync(path.join(releaseDir, 'manifest.json'), manifestBytes);
  fs.writeFileSync(path.join(releaseDir, 'manifest.sig'), crypto.sign(null, manifestBytes, privateKey));
  const preload = path.join(directory, 'mock-https.js');
  fs.writeFileSync(preload, `'use strict';
const fs = require('fs');
const path = require('path');
const https = require('https');
const { EventEmitter } = require('events');
const { PassThrough } = require('stream');
const original = https.get;
https.get = function (url, options, callback) {
  if (new URL(url).hostname !== 'bp.tunapro.xyz') return original.call(this, url, options, callback);
  const request = new EventEmitter();
  request.setTimeout = () => request;
  request.destroy = (error) => { if (error) queueMicrotask(() => request.emit('error', error)); };
  queueMicrotask(() => {
    const file = path.join(process.env.BP_TEST_RELEASE_DIR, path.basename(new URL(url).pathname));
    const response = new PassThrough();
    response.statusCode = fs.existsSync(file) ? 200 : 404;
    response.headers = {};
    callback(response);
    response.end(fs.existsSync(file) ? fs.readFileSync(file) : Buffer.alloc(0));
  });
  return request;
};
`);
  const packed = JSON.parse(childProcess.execFileSync('npm', [
    'pack', '--ignore-scripts', '--json', '--pack-destination', packDirectory
  ], { cwd: source, encoding: 'utf8' }))[0];
  assert.deepEqual(packed.files.map((entry) => entry.path).sort(), [...PACKED_FILES].sort());

  const tarball = path.join(packDirectory, packed.filename);
  const installEnv = {
    ...process.env,
    BP_TEST_RELEASE_DIR: releaseDir,
    NODE_OPTIONS: `${process.env.NODE_OPTIONS || ''} --require=${preload}`.trim()
  };
  childProcess.execFileSync('npm', [
    'install', '--global', '--offline', '--no-audit', '--no-fund', '--prefix', prefix, tarball
  ], { env: installEnv, stdio: 'pipe' });
  const packageDir = path.join(prefix, 'lib', 'node_modules', '@tunapro', 'blueprint');
  const shim = path.join(prefix, 'bin', 'bp');
  assert.equal(fs.existsSync(path.join(packageDir, 'install.js')), true);
  assert.equal(fs.existsSync(shim), true);
  const installedBinary = path.join(packageDir, 'bin', filename);
  assert.deepEqual(fs.readFileSync(installedBinary), binary);
  assert.equal(fs.statSync(installedBinary).mode & 0o777, 0o755);
  const launched = childProcess.spawnSync(shim, ['hello', 'two words'], { encoding: 'utf8' });
  assert.equal(launched.status, 0, launched.stderr);
  assert.equal(launched.stdout.trim(), 'fixture native hello|two words');

  childProcess.execFileSync('npm', [
    'uninstall', '--global', '--offline', '--no-audit', '--no-fund', '--prefix', prefix, '@tunapro/blueprint'
  ], { env: installEnv, stdio: 'pipe' });
  assert.equal(fs.existsSync(packageDir), false);
  assert.equal(fs.existsSync(shim), false);
});

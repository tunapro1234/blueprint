#!/usr/bin/env node

'use strict';

const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const childProcess = require('child_process');
const { BASE_URL, MAX_BINARY_BYTES, download, expectedHash, verifyManifest } = require('../install');

const ARTIFACTS = [
  'bp-linux-amd64',
  'bp-linux-arm64',
  'bp-darwin-amd64',
  'bp-darwin-arm64'
];
const PACKED_FILES = ['LICENSE', 'README.md', 'bin/bp.js', 'install.js', 'package.json', 'release.pub'];

function packageDetails(packageDir) {
  const pkg = JSON.parse(fs.readFileSync(path.join(packageDir, 'package.json'), 'utf8'));
  if (!/^\d+\.\d+\.\d+$/.test(pkg.version)) throw new Error('package version must be x.y.z');
  return pkg;
}

function verifyCheckout(packageDir, version, publicKey) {
  const root = path.resolve(packageDir, '..');
  const nativeVersionPath = path.join(root, 'internal', 'release', 'version.txt');
  const nativeKeyPath = path.join(root, 'internal', 'release', 'release.pub');
  if (!fs.existsSync(nativeVersionPath) && !fs.existsSync(nativeKeyPath)) return;
  if (!fs.existsSync(nativeVersionPath) || !fs.existsSync(nativeKeyPath)) {
    throw new Error('checkout has an incomplete native release source');
  }
  if (fs.readFileSync(nativeVersionPath, 'utf8').trim() !== version) throw new Error('npm/native version mismatch');
  if (!fs.readFileSync(nativeKeyPath).equals(publicKey)) throw new Error('npm/native public key mismatch');
}

function verifyPackedLayout(packageDir, execute = childProcess.execFileSync) {
  const output = execute('npm', ['pack', '--dry-run', '--ignore-scripts', '--json'], {
    cwd: packageDir,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe']
  });
  const report = JSON.parse(output);
  const actual = report[0].files.map((entry) => entry.path).sort();
  const expected = [...PACKED_FILES].sort();
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`unexpected packed files: ${actual.join(', ')}`);
  }
}

async function verifyPublishedRelease(options = {}) {
  const packageDir = options.packageDir || path.resolve(__dirname, '..');
  const fetch = options.download || download;
  const baseUrl = (options.baseUrl || BASE_URL).replace(/\/$/, '');
  const pkg = packageDetails(packageDir);
  const key = fs.readFileSync(path.join(packageDir, 'release.pub'));
  verifyCheckout(packageDir, pkg.version, key);
  if (!options.skipPackCheck) verifyPackedLayout(packageDir, options.execute);

  const deadline = Date.now() + (options.timeoutMs || 30000);
  const releaseUrl = `${baseUrl}/releases/v${pkg.version}`;
  const [manifestBytes, signature] = await Promise.all([
    fetch(`${releaseUrl}/manifest.json`, { deadline, maxBytes: 64 * 1024 }),
    fetch(`${releaseUrl}/manifest.sig`, { deadline, maxBytes: 1024 })
  ]);
  const manifest = verifyManifest(manifestBytes, signature, key, pkg.version);
  for (const filename of ARTIFACTS) {
    const expected = expectedHash(manifest, filename);
    const binary = await fetch(`${releaseUrl}/${filename}`, { deadline, maxBytes: MAX_BINARY_BYTES });
    const actual = crypto.createHash('sha256').update(binary).digest('hex');
    if (actual !== expected) throw new Error(`SHA-256 mismatch for ${filename}`);
  }
  return { version: pkg.version, revision: manifest.revision };
}

async function main() {
  try {
    const result = await verifyPublishedRelease();
    console.log(`npm publish gate verified signed native release ${result.version} (${result.revision}).`);
  } catch (error) {
    console.error(`npm publish blocked: ${error.message}`);
    process.exitCode = 1;
  }
}

module.exports = {
  ARTIFACTS,
  PACKED_FILES,
  packageDetails,
  verifyCheckout,
  verifyPackedLayout,
  verifyPublishedRelease
};

if (require.main === module) main();

#!/usr/bin/env node

'use strict';

const crypto = require('crypto');
const fs = require('fs');
const https = require('https');
const path = require('path');

const BASE_URL = 'https://bp.tunapro.xyz';
const DEFAULT_TIMEOUT_MS = 30000;
const MAX_BINARY_BYTES = 64 * 1024 * 1024;

function platformBinary(platform = process.platform, architecture = process.arch) {
  const operatingSystems = { linux: 'linux', darwin: 'darwin' };
  const architectures = { x64: 'amd64', arm64: 'arm64' };
  const os = operatingSystems[platform];
  const arch = architectures[architecture];
  if (!os || !arch) throw new Error(`unsupported platform: ${platform}-${architecture}`);
  return `bp-${os}-${arch}`;
}

function download(url, options = {}) {
  const get = options.get || https.get;
  const maxBytes = options.maxBytes || MAX_BINARY_BYTES;
  const maxRedirects = options.maxRedirects === undefined ? 5 : options.maxRedirects;
  const redirects = options.redirects || 0;
  const deadline = options.deadline || Date.now() + (options.timeoutMs || DEFAULT_TIMEOUT_MS);

  return new Promise((resolve, reject) => {
    const remaining = deadline - Date.now();
    if (remaining <= 0) {
      reject(new Error('download timed out'));
      return;
    }

    let request;
    let settled = false;
    let redirecting = false;
    const finish = (error, value) => {
      if (settled || redirecting) return;
      settled = true;
      clearTimeout(timer);
      if (error) reject(error);
      else resolve(value);
    };
    const timer = setTimeout(() => {
      if (request) request.destroy(new Error('download timed out'));
      else finish(new Error('download timed out'));
    }, remaining);

    try {
      request = get(url, { headers: { 'user-agent': '@tunapro/blueprint installer' } }, (response) => {
        if (response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
          response.resume();
          if (redirects >= maxRedirects) {
            finish(new Error('too many redirects'));
            return;
          }
          let next;
          try {
            next = new URL(response.headers.location, url);
          } catch (error) {
            finish(new Error(`invalid redirect URL: ${error.message}`));
            return;
          }
          if (next.protocol !== 'https:') {
            finish(new Error(`refusing non-HTTPS redirect to ${next.href}`));
            return;
          }
          redirecting = true;
          clearTimeout(timer);
          download(next.href, { ...options, redirects: redirects + 1, deadline }).then(resolve, reject);
          return;
        }
        if (response.statusCode !== 200) {
          response.resume();
          finish(new Error(`download returned HTTP ${response.statusCode}`));
          return;
        }

        const chunks = [];
        let size = 0;
        response.on('data', (chunk) => {
          size += chunk.length;
          if (size > maxBytes) {
            finish(new Error(`download exceeded ${maxBytes} bytes`));
            if (request) request.destroy();
            response.destroy();
            return;
          }
          chunks.push(chunk);
        });
        response.on('end', () => finish(null, Buffer.concat(chunks)));
        response.on('aborted', () => finish(new Error('download was interrupted')));
        response.on('error', (error) => finish(error));
      });
      request.setTimeout(remaining, () => request.destroy(new Error('download timed out')));
      request.on('error', (error) => finish(error));
    } catch (error) {
      finish(error);
    }
  });
}

function verifyManifest(manifestBytes, signature, key, version) {
  if (!crypto.verify(null, manifestBytes, key, signature)) throw new Error('release signature failed');
  const manifest = JSON.parse(manifestBytes.toString('utf8'));
  if (manifest.version !== version) throw new Error('release version mismatch');
  if (!manifest.sha256 || typeof manifest.sha256 !== 'object' || Array.isArray(manifest.sha256)) {
    throw new Error('release manifest has no checksum map');
  }
  return manifest;
}

function expectedHash(manifest, filename) {
  const value = manifest.sha256[filename];
  if (typeof value !== 'string' || !/^[a-f0-9]{64}$/.test(value)) {
    throw new Error(`${filename} has no valid SHA-256 checksum`);
  }
  return value;
}

async function install(options = {}) {
  const packageDir = options.packageDir || __dirname;
  const baseUrl = (options.baseUrl || BASE_URL).replace(/\/$/, '');
  const fetch = options.download || download;
  const timeoutMs = options.timeoutMs || DEFAULT_TIMEOUT_MS;
  const deadline = Date.now() + timeoutMs;
  const filename = platformBinary(options.platform, options.architecture);
  const version = JSON.parse(fs.readFileSync(path.join(packageDir, 'package.json'), 'utf8')).version;
  const releaseUrl = `${baseUrl}/releases/v${version}`;
  const [manifestBytes, signature] = await Promise.all([
    fetch(`${releaseUrl}/manifest.json`, { deadline, maxBytes: 64 * 1024 }),
    fetch(`${releaseUrl}/manifest.sig`, { deadline, maxBytes: 1024 })
  ]);
  const key = fs.readFileSync(path.join(packageDir, 'release.pub'));
  const manifest = verifyManifest(manifestBytes, signature, key, version);
  const expected = expectedHash(manifest, filename);
  const binary = await fetch(`${releaseUrl}/${filename}`, { deadline, maxBytes: MAX_BINARY_BYTES });
  const actual = crypto.createHash('sha256').update(binary).digest('hex');
  if (actual !== expected) throw new Error(`SHA-256 mismatch for ${filename}`);

  const target = path.join(packageDir, 'bin', filename);
  const temporary = `${target}.tmp-${process.pid}`;
  try {
    fs.writeFileSync(temporary, binary, { mode: 0o755 });
    fs.renameSync(temporary, target);
    fs.chmodSync(target, 0o755);
  } finally {
    try {
      fs.unlinkSync(temporary);
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
  }
  return { filename, target };
}

async function main() {
  try {
    const result = await install();
    console.log(`Installed ${result.filename} (signature and SHA-256 verified).`);
    console.log('Run bp setup, then bp onboard in a terminal to configure local agent sessions.');
  } catch (error) {
    console.error(`bp installation failed: ${error.message}`);
    process.exitCode = 1;
  }
}

module.exports = {
  BASE_URL,
  DEFAULT_TIMEOUT_MS,
  MAX_BINARY_BYTES,
  download,
  expectedHash,
  install,
  platformBinary,
  verifyManifest
};

if (require.main === module) main();

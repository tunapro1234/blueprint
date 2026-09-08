#!/usr/bin/env node

'use strict';

const crypto = require('crypto');
const fs = require('fs');
const https = require('https');
const path = require('path');

const baseUrl = 'https://bp.tunapro.xyz';

function platformBinary() {
  const operatingSystems = { linux: 'linux', darwin: 'darwin' };
  const architectures = { x64: 'amd64', arm64: 'arm64' };
  const os = operatingSystems[process.platform];
  const arch = architectures[process.arch];
  if (!os || !arch) {
    throw new Error(`unsupported platform: ${process.platform}-${process.arch}`);
  }
  return `bp-${os}-${arch}`;
}

function download(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 5) {
      reject(new Error('too many redirects'));
      return;
    }
    const request = https.get(url, { headers: { 'user-agent': '@tunapro/blueprint installer' } }, (response) => {
      if (response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
        response.resume();
        const next = new URL(response.headers.location, url);
        if (next.protocol !== 'https:') {
          reject(new Error(`refusing non-HTTPS redirect to ${next.href}`));
          return;
        }
        download(next.href, redirects + 1).then(resolve, reject);
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`download returned HTTP ${response.statusCode}`));
        return;
      }
      const chunks = [];
      let size = 0;
      response.on('data', (chunk) => {
        size += chunk.length;
        if (size > 64 * 1024 * 1024) {
          request.destroy(new Error('download exceeded 64 MiB'));
          return;
        }
        chunks.push(chunk);
      });
      response.on('end', () => resolve(Buffer.concat(chunks)));
      response.on('error', reject);
    });
    request.setTimeout(30000, () => request.destroy(new Error('download timed out')));
    request.on('error', reject);
  });
}

function expectedChecksum(checksums, filename) {
  for (const line of checksums.split(/\r?\n/)) {
    const match = line.match(/^([a-fA-F0-9]{64})\s+\*?(.+)$/);
    if (match && match[2].trim() === filename) {
      return match[1].toLowerCase();
    }
  }
  throw new Error(`${filename} is missing from checksums.txt`);
}

function printIntegrationNotes() {
  console.log('Run bp setup, then bp onboard in a terminal to configure local agent sessions.');
}

async function install() {
  let filename;
  try {
    filename = platformBinary();
    const version = require('./package.json').version;
    const releaseUrl = `${baseUrl}/releases/v${version}`;
    const [manifestBytes, signature] = await Promise.all([
      download(`${releaseUrl}/manifest.json`), download(`${releaseUrl}/manifest.sig`)
    ]);
    const key = fs.readFileSync(path.join(__dirname, 'release.pub'));
    if (!crypto.verify(null, manifestBytes, key, signature)) throw new Error('release signature failed');
    const manifest = JSON.parse(manifestBytes);
    if (manifest.version !== version) throw new Error('release version mismatch');
    const binary = await download(`${releaseUrl}/${filename}`);
    const actual = crypto.createHash('sha256').update(binary).digest('hex');
    if (actual !== manifest.sha256[filename]) throw new Error(`SHA-256 mismatch for ${filename}`);

    const target = path.join(__dirname, 'bin', filename);
    const temporary = `${target}.tmp-${process.pid}`;
    fs.writeFileSync(temporary, binary, { mode: 0o755 });
    fs.renameSync(temporary, target);
    fs.chmodSync(target, 0o755);
    console.log(`Installed ${filename} (SHA-256 verified).`);
    printIntegrationNotes();
  } catch (error) {
    console.error(`bp installation failed: ${error.message}`);
    process.exitCode = 1;
  }
}

install();

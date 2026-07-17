#!/usr/bin/env node

'use strict';

const fs = require('fs');
const path = require('path');
const childProcess = require('child_process');

const operatingSystems = { linux: 'linux', darwin: 'darwin' };
const architectures = { x64: 'amd64', arm64: 'arm64' };
const os = operatingSystems[process.platform];
const arch = architectures[process.arch];
const filename = os && arch ? `bp-${os}-${arch}` : null;
const binary = filename ? path.join(__dirname, filename) : null;

if (!binary || !fs.existsSync(binary)) {
  const suffix = filename || 'bp-<os>-<arch>';
  console.error(`The native bp binary is unavailable for ${process.platform}-${process.arch}.

Reinstall @tunapro/blueprint, or install it manually:
  mkdir -p ~/.local/bin
  curl -fsSL https://bp.tunapro.xyz/${suffix} -o ~/.local/bin/bp
  chmod 755 ~/.local/bin/bp

Verify it against https://bp.tunapro.xyz/checksums.txt and add ~/.local/bin to PATH.`);
  process.exit(1);
}

const result = childProcess.spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(`Could not run ${binary}: ${result.error.message}`);
  process.exit(1);
}
if (result.signal) {
  process.kill(process.pid, result.signal);
}
process.exit(result.status === null ? 1 : result.status);

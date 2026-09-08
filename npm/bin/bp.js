#!/usr/bin/env node

'use strict';

const fs = require('fs');
const path = require('path');
const childProcess = require('child_process');

function platformFilename(platform = process.platform, architecture = process.arch) {
  const operatingSystems = { linux: 'linux', darwin: 'darwin' };
  const architectures = { x64: 'amd64', arm64: 'arm64' };
  const os = operatingSystems[platform];
  const arch = architectures[architecture];
  return os && arch ? `bp-${os}-${arch}` : null;
}

function launch(args, options = {}) {
  const platform = options.platform || process.platform;
  const architecture = options.architecture || process.arch;
  const filename = platformFilename(platform, architecture);
  const directory = options.directory || __dirname;
  const binary = filename ? path.join(directory, filename) : null;
  if (!binary || !fs.existsSync(binary)) {
    return {
      status: 1,
      error: `The native bp binary is unavailable for ${platform}-${architecture}.\n` +
        'Reinstall @tunapro/blueprint with install scripts enabled, or use the signed shell installer:\n' +
        '  curl -fsSL https://bp.tunapro.xyz/install.sh | sh -s -- --local'
    };
  }

  const spawnSync = options.spawnSync || childProcess.spawnSync;
  const env = { ...(options.env || process.env), BP_LAUNCHER_PATH: fs.realpathSync(options.launcherPath || __filename) };
  const result = spawnSync(binary, args, { stdio: 'inherit', env });
  if (result.error) return { status: 1, error: `Could not run ${binary}: ${result.error.message}` };
  return { status: result.status === null ? 1 : result.status, signal: result.signal };
}

function main() {
  const result = launch(process.argv.slice(2));
  if (result.error) console.error(result.error);
  if (result.signal) process.kill(process.pid, result.signal);
  process.exit(result.status);
}

module.exports = { launch, platformFilename };

if (require.main === module) main();

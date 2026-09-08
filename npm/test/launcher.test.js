'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const childProcess = require('node:child_process');
const test = require('node:test');
const { launch, platformFilename } = require('../bin/bp');

test('launcher forwards argv, exit status, and its verified path', { skip: !platformFilename() }, (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'bp-npm-launcher-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const wrapper = path.join(directory, 'bp.js');
  const binary = path.join(directory, platformFilename());
  const capture = path.join(directory, 'capture.json');
  fs.copyFileSync(path.resolve(__dirname, '..', 'bin', 'bp.js'), wrapper);
  fs.writeFileSync(binary, `#!/usr/bin/env node
const fs = require('fs');
fs.writeFileSync(process.env.BP_TEST_CAPTURE, JSON.stringify({
  args: process.argv.slice(2),
  launcher: process.env.BP_LAUNCHER_PATH
}));
process.exit(37);
`, { mode: 0o755 });

  const result = childProcess.spawnSync(process.execPath, [wrapper, 'alpha', 'two words'], {
    env: { ...process.env, BP_TEST_CAPTURE: capture },
    encoding: 'utf8'
  });
  assert.equal(result.status, 37, result.stderr);
  assert.deepEqual(JSON.parse(fs.readFileSync(capture, 'utf8')), {
    args: ['alpha', 'two words'],
    launcher: fs.realpathSync(wrapper)
  });
});

test('launcher reports unsupported or missing native binaries', () => {
  const unsupported = launch([], { platform: 'win32', architecture: 'x64' });
  assert.equal(unsupported.status, 1);
  assert.match(unsupported.error, /unavailable for win32-x64/);
});

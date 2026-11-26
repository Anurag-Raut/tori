#!/usr/bin/env node
"use strict";

const { spawnSync } = require("node:child_process");

const PLATFORMS = {
  "linux-x64": "tori-cli-linux-x64",
  "linux-arm64": "tori-cli-linux-arm64",
  "darwin-x64": "tori-cli-darwin-x64",
  "darwin-arm64": "tori-cli-darwin-arm64",
};

const key = `${process.platform}-${process.arch}`;
const pkg = PLATFORMS[key];

if (!pkg) {
  console.error(
    `tori-cli: unsupported platform ${process.platform}/${process.arch} (supported: linux/darwin on x64/arm64).`
  );
  process.exit(1);
}

let binPath;
try {
  binPath = require.resolve(`${pkg}/bin/tori`);
} catch {
  console.error(
    `tori-cli: platform package '${pkg}' is not installed.\n` +
      `Reinstall without --no-optional, or install directly:\n` +
      `  curl -fsSL https://raw.githubusercontent.com/Anurag-Raut/tori/main/install.sh | sh`
  );
  process.exit(1);
}

const result = spawnSync(binPath, process.argv.slice(2), {
  stdio: "inherit",
});

if (result.error) {
  console.error(`tori-cli: failed to launch ${binPath}: ${result.error.message}`);
  process.exit(1);
}

if (result.signal) {
  process.kill(process.pid, result.signal);
}

process.exit(result.status ?? 1);

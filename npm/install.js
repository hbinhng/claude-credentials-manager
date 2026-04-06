#!/usr/bin/env node

const https = require("https");
const fs = require("fs");
const path = require("path");

const ASSETS = {
  "darwin-x64": "ccm-darwin-amd64",
  "darwin-arm64": "ccm-darwin-arm64",
  "linux-x64": "ccm-linux-amd64",
  "linux-arm64": "ccm-linux-arm64",
  "win32-x64": "ccm-windows-amd64.exe",
  "win32-arm64": "ccm-windows-arm64.exe",
};

const key = `${process.platform}-${process.arch}`;
const asset = ASSETS[key];
if (!asset) {
  console.error(`Unsupported platform: ${key}`);
  process.exit(1);
}

const { version } = require("./package.json");
const url = `https://github.com/hbinhng/claude-credentials-manager/releases/download/v${version}/${asset}`;

const binDir = path.join(__dirname, "bin");
const isWin = process.platform === "win32";
const binName = isWin ? "ccm.exe" : "ccm";
const binPath = path.join(binDir, binName);

fs.mkdirSync(binDir, { recursive: true });

function download(url) {
  return new Promise((resolve, reject) => {
    https.get(url, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        return download(res.headers.location).then(resolve, reject);
      }
      if (res.statusCode !== 200) {
        return reject(new Error(`Download failed: HTTP ${res.statusCode}`));
      }
      const chunks = [];
      res.on("data", (chunk) => chunks.push(chunk));
      res.on("end", () => resolve(Buffer.concat(chunks)));
      res.on("error", reject);
    }).on("error", reject);
  });
}

download(url)
  .then((data) => {
    fs.writeFileSync(binPath, data);
    if (!isWin) {
      fs.chmodSync(binPath, 0o755);
    }
    console.log(`ccm v${version} installed (${key})`);
  })
  .catch((err) => {
    console.error(`Failed to install ccm: ${err.message}`);
    process.exit(1);
  });

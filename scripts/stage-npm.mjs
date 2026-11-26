#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, "..");

const TARGETS = [
  { pkg: "tori-switch-linux-x64", os: "linux", cpu: "x64", goos: "linux", goarch: "amd64", bin: "tori" },
  { pkg: "tori-switch-linux-arm64", os: "linux", cpu: "arm64", goos: "linux", goarch: "arm64", bin: "tori" },
  { pkg: "tori-switch-darwin-x64", os: "darwin", cpu: "x64", goos: "darwin", goarch: "amd64", bin: "tori" },
  { pkg: "tori-switch-darwin-arm64", os: "darwin", cpu: "arm64", goos: "darwin", goarch: "arm64", bin: "tori" },
  { pkg: "@anurag-raut/tori-switch-win32-x64", os: "win32", cpu: "x64", goos: "windows", goarch: "amd64", bin: "tori.exe" },
  { pkg: "@anurag-raut/tori-switch-win32-arm64", os: "win32", cpu: "arm64", goos: "windows", goarch: "arm64", bin: "tori.exe" },
];

function parseArgs(argv) {
  const opts = {
    version: "",
    dist: path.join(repoRoot, "dist"),
    out: path.join(repoRoot, "dist", "npm"),
  };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--version" && argv[i + 1]) {
      opts.version = argv[++i].replace(/^v/, "");
    } else if (a === "--dist" && argv[i + 1]) {
      opts.dist = path.resolve(argv[++i]);
    } else if (a === "--out" && argv[i + 1]) {
      opts.out = path.resolve(argv[++i]);
    } else {
      throw new Error(`unknown argument: ${a}`);
    }
  }
  if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(opts.version)) {
    throw new Error(`--version must be valid semver (e.g. 0.2.0), got '${opts.version}'`);
  }
  return opts;
}

function findBinary(distDir, goos, goarch, binName) {
  const prefix = `tori_${goos}_${goarch}`;
  const entries = fs.readdirSync(distDir, { withFileTypes: true });
  for (const entry of entries) {
    if (!entry.isDirectory()) continue;
    if (entry.name === prefix || entry.name.startsWith(`${prefix}_`)) {
      const candidate = path.join(distDir, entry.name, binName);
      if (fs.existsSync(candidate)) {
        return candidate;
      }
    }
  }
  throw new Error(`missing GoReleaser binary for ${goos}/${goarch} (${binName}) under ${distDir}`);
}

function main() {
  const { version, dist, out } = parseArgs(process.argv.slice(2));
  const templateDir = path.join(repoRoot, "npm", "tori-switch");
  const mainPkg = JSON.parse(fs.readFileSync(path.join(templateDir, "package.json"), "utf8"));
  const licensePath = path.join(repoRoot, "LICENSE");
  const readmePath = path.join(repoRoot, "README.md");

  fs.rmSync(out, { recursive: true, force: true });
  fs.mkdirSync(out, { recursive: true });

  // 1. Stage each platform binary package.
  for (const t of TARGETS) {
    const binSrc = findBinary(dist, t.goos, t.goarch, t.bin);
    const pkgDir = path.join(out, t.pkg);
    const binDir = path.join(pkgDir, "bin");
    fs.mkdirSync(binDir, { recursive: true });

    const binDst = path.join(binDir, t.bin);
    fs.copyFileSync(binSrc, binDst);
    if (t.os !== "win32") {
      fs.chmodSync(binDst, 0o755);
    }
    fs.copyFileSync(licensePath, path.join(pkgDir, "LICENSE"));

    const manifest = {
      name: t.pkg,
      version,
      description: `${mainPkg.description} (${t.os}/${t.cpu} binary)`,
      license: mainPkg.license,
      repository: mainPkg.repository,
      homepage: mainPkg.homepage,
      bugs: mainPkg.bugs,
      os: [t.os],
      cpu: [t.cpu],
      files: [`bin/${t.bin}`, "LICENSE"],
    };
    fs.writeFileSync(
      path.join(pkgDir, "package.json"),
      `${JSON.stringify(manifest, null, 2)}\n`
    );
  }

  // 2. Stage the main wrapper package.
  const mainOut = path.join(out, mainPkg.name);
  fs.mkdirSync(path.join(mainOut, "bin"), { recursive: true });

  const shimSrc = path.join(templateDir, "bin", "tori.js");
  const shimDst = path.join(mainOut, "bin", "tori.js");
  fs.copyFileSync(shimSrc, shimDst);
  fs.chmodSync(shimDst, 0o755);
  fs.copyFileSync(licensePath, path.join(mainOut, "LICENSE"));

  const tag = `v${version}`;
  const rawBase = `https://raw.githubusercontent.com/Anurag-Raut/tori/${tag}`;
  const blobBase = `https://github.com/Anurag-Raut/tori/blob/${tag}`;
  const npmReadme = fs
    .readFileSync(readmePath, "utf8")
    .replaceAll('src="docs/', `src="${rawBase}/docs/`)
    .replaceAll('href="LICENSE"', `href="${blobBase}/LICENSE"`)
    .replaceAll('href="go.mod"', `href="${blobBase}/go.mod"`)
    .replaceAll("](LICENSE)", `](${blobBase}/LICENSE)`);
  fs.writeFileSync(path.join(mainOut, "README.md"), npmReadme);

  mainPkg.version = version;
  for (const t of TARGETS) {
    mainPkg.optionalDependencies[t.pkg] = version;
  }
  fs.writeFileSync(
    path.join(mainOut, "package.json"),
    `${JSON.stringify(mainPkg, null, 2)}\n`
  );

  console.log(`Staged ${TARGETS.length + 1} npm packages for ${mainPkg.name}@${version} in ${out}`);
}

main();

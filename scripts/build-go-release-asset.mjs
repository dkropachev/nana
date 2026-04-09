import { mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createHash } from "node:crypto";
import { tmpdir } from "node:os";
import { basename, join, resolve } from "node:path";
import { spawnSync } from "node:child_process";

function usage() {
  console.error("Usage: node scripts/build-go-release-asset.mjs --target <triple> --out-dir <dir>");
  process.exit(1);
}

function arg(name) {
  const index = process.argv.indexOf(name);
  if (index < 0) return undefined;
  return process.argv[index + 1];
}

function mapTriple(target) {
  switch (target) {
    case "x86_64-unknown-linux-gnu":
    case "x86_64-unknown-linux-musl":
      return { goos: "linux", goarch: "amd64", ext: "" };
    case "aarch64-unknown-linux-gnu":
    case "aarch64-unknown-linux-musl":
      return { goos: "linux", goarch: "arm64", ext: "" };
    case "x86_64-apple-darwin":
      return { goos: "darwin", goarch: "amd64", ext: "" };
    case "aarch64-apple-darwin":
      return { goos: "darwin", goarch: "arm64", ext: "" };
    case "x86_64-pc-windows-msvc":
      return { goos: "windows", goarch: "amd64", ext: ".exe" };
    default:
      throw new Error(`unsupported go release target: ${target}`);
  }
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

const target = arg("--target");
const outDirArg = arg("--out-dir");
if (!target || !outDirArg) usage();

const root = process.cwd();
const outDir = resolve(outDirArg);
mkdirSync(outDir, { recursive: true });

const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf-8"));
const version = pkg.version;
const { goos, goarch, ext } = mapTriple(target);

const stagingRoot = join(tmpdir(), `nana-go-release-${process.pid}-${Date.now()}`);
const stagingDir = join(stagingRoot, `nana-${target}`);
mkdirSync(stagingDir, { recursive: true });

const binaries = [
  { pkg: './cmd/nana', name: `nana${ext}` },
  { pkg: './cmd/nana-runtime', name: `nana-runtime${ext}` },
  { pkg: './cmd/nana-explore', name: `nana-explore-harness${ext}` },
  { pkg: './cmd/nana-sparkshell', name: `nana-sparkshell${ext}` },
];
const binaryName = binaries[0].name;
const binaryPath = join(stagingDir, binaryName);
const archiveName = goos === "windows"
  ? `nana-${target}.zip`
  : `nana-${target}.tar.gz`;
const archivePath = join(outDir, archiveName);
const checksumPath = join(outDir, `${archiveName}.sha256`);
const metadataPath = join(outDir, `nana-${target}.metadata.json`);

try {
  for (const binary of binaries) {
    const build = spawnSync(
      "go",
      [
        "build",
        "-ldflags",
        `-X github.com/Yeachan-Heo/nana/internal/version.Version=${version}`,
        "-o",
        join(stagingDir, binary.name),
        binary.pkg,
      ],
      {
        cwd: root,
        stdio: "pipe",
        encoding: "utf-8",
        env: {
          ...process.env,
          GOOS: goos,
          GOARCH: goarch,
          CGO_ENABLED: "0",
        },
      },
    );
    if (build.status !== 0) {
      throw new Error(build.stderr || build.stdout || `go build failed for ${target} (${binary.pkg})`);
    }
  }

  let archiveResult;
  if (goos === "windows") {
    archiveResult = spawnSync(
      "powershell",
      [
        "-NoLogo",
        "-NoProfile",
        "-ExecutionPolicy",
        "Bypass",
        "-Command",
        `Compress-Archive -LiteralPath '${stagingDir.replace(/'/g, "''")}\\*' -DestinationPath '${archivePath.replace(/'/g, "''")}' -Force`,
      ],
      { stdio: "pipe", encoding: "utf-8" },
    );
  } else {
    archiveResult = spawnSync(
      "tar",
      ["-czf", archivePath, "-C", stagingDir, ...binaries.map((binary) => basename(join(stagingDir, binary.name)))],
      { stdio: "pipe", encoding: "utf-8" },
    );
  }
  if (archiveResult.status !== 0) {
    throw new Error(archiveResult.stderr || archiveResult.stdout || `archive build failed for ${target}`);
  }

  const digest = sha256(archivePath);
  writeFileSync(checksumPath, `${digest}\n`);
  writeFileSync(metadataPath, `${JSON.stringify({
    product: "nana",
    version,
    target,
    archive: archiveName,
    binary: binaryName,
    binary_path: binaryName,
    sha256: digest,
    size: statSync(archivePath).size,
  }, null, 2)}\n`);

  console.log(archivePath);
} finally {
  rmSync(stagingRoot, { recursive: true, force: true });
}

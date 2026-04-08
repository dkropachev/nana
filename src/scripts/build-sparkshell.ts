import { chmodSync, copyFileSync, existsSync, mkdirSync } from 'node:fs';
import { arch, platform } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const projectRoot = join(__dirname, '..', '..');
const nativeRoot = join(projectRoot, 'crates', 'nana-sparkshell');
const manifestPath = process.env.NANA_SPARKSHELL_MANIFEST ?? join(nativeRoot, 'Cargo.toml');
const binaryName = platform() === 'win32' ? 'nana-sparkshell.exe' : 'nana-sparkshell';
const releaseBinaryPath = join(projectRoot, 'target', 'release', binaryName);
const stagedBinaryRoot = process.env.NANA_SPARKSHELL_STAGE_DIR
  ? join(process.env.NANA_SPARKSHELL_STAGE_DIR, `${platform()}-${arch()}`)
  : join(projectRoot, 'bin', 'native', `${platform()}-${arch()}`);
const packagedBinaryDir = stagedBinaryRoot;
const packagedBinaryPath = join(packagedBinaryDir, binaryName);
const extraArgs = process.argv.slice(2);
const args = ['build', '--manifest-path', manifestPath, '--release', ...extraArgs];

if (!existsSync(manifestPath)) {
  console.error(`nana sparkshell build: missing Rust manifest at ${manifestPath}`);
  process.exit(1);
}

const result = spawnSync('cargo', args, {
  cwd: projectRoot,
  stdio: 'inherit',
  env: process.env,
});

if (result.error) {
  console.error(`nana sparkshell build: failed to launch cargo: ${result.error.message}`);
  process.exit(1);
}

if ((result.status ?? 1) !== 0) {
  process.exit(result.status ?? 1);
}

if (!existsSync(releaseBinaryPath)) {
  console.error(`nana sparkshell build: expected release binary at ${releaseBinaryPath}`);
  process.exit(1);
}

mkdirSync(packagedBinaryDir, { recursive: true });
copyFileSync(releaseBinaryPath, packagedBinaryPath);
if (platform() !== 'win32') {
  chmodSync(packagedBinaryPath, 0o755);
}
console.log(`nana sparkshell build: staged native binary at ${packagedBinaryPath}`);

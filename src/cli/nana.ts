#!/usr/bin/env node

// nana CLI entry point
// Requires compiled JavaScript output in dist/

import { fileURLToPath, pathToFileURL } from 'url';
import { dirname, join } from 'path';
import { existsSync } from 'fs';
import { spawnSync } from 'child_process';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const root = join(__dirname, '..', '..');
const goBinary = join(root, 'bin', process.platform === 'win32' ? 'nana.exe' : 'nana');
const goShimActive = process.env.NANA_GO_SHIM_ACTIVE === '1';

if (!goShimActive && existsSync(goBinary)) {
  const result = spawnSync(goBinary, process.argv.slice(2), {
    stdio: 'inherit',
    env: process.env,
  });
  if (result.error) {
    console.error(`nana: failed to launch preferred Go CLI: ${result.error.message}`);
    process.exit(1);
  }
  if (typeof result.status === 'number') {
    process.exit(result.status);
  }
  process.exit(1);
}

const hydratedBinary = goShimActive
  ? undefined
  : await import('./native-assets.js')
    .then(({ hydrateNativeBinary }) => hydrateNativeBinary('nana', {
      packageRoot: root,
      env: process.env,
    }))
    .catch(() => undefined);

if (!goShimActive && hydratedBinary && existsSync(hydratedBinary)) {
  const result = spawnSync(hydratedBinary, process.argv.slice(2), {
    stdio: 'inherit',
    env: process.env,
  });
  if (result.error) {
    console.error(`nana: failed to launch hydrated Go CLI: ${result.error.message}`);
    process.exit(1);
  }
  if (typeof result.status === 'number') {
    process.exit(result.status);
  }
  process.exit(1);
}

// Fallback to compiled JavaScript entrypoint
const distEntry = join(root, 'dist', 'cli', 'index.js');

if (existsSync(distEntry)) {
  const { main } = await import(pathToFileURL(distEntry).href);
  await main(process.argv.slice(2));
  process.exit(process.exitCode ?? 0);
} else {
  console.error('nana: run "npm run build" first');
  process.exit(1);
}

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, copyFileSync, mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createServer } from 'node:http';

const wrapperTimeoutMs = 15000;

function sha256(buffer: Buffer): string {
  return createHash('sha256').update(buffer).digest('hex');
}

describe('nana wrapper', () => {
  it('does not recurse back into the Go binary when the legacy Go shim hands off to JS', () => {
    const root = mkdtempSync(join(tmpdir(), 'nana-wrapper-go-shim-'));
    try {
      const distCliDir = join(root, 'dist', 'cli');
      const binDir = join(root, 'bin');
      mkdirSync(distCliDir, { recursive: true });
      mkdirSync(binDir, { recursive: true });

      copyFileSync(join(process.cwd(), 'dist', 'cli', 'nana.js'), join(distCliDir, 'nana.js'));
      writeFileSync(
        join(distCliDir, 'index.js'),
        [
          'export async function main(args) {',
          "  process.stdout.write(`shim-fallback:${args.join(' ')}\\n`);",
          '}',
        ].join('\n'),
        'utf-8',
      );

      const goBinaryPath = join(binDir, process.platform === 'win32' ? 'nana.exe' : 'nana');
      if (process.platform === 'win32') {
        writeFileSync(goBinaryPath, '@echo off\r\necho should-not-run-go:%*\r\n', 'utf-8');
      } else {
        writeFileSync(goBinaryPath, '#!/bin/sh\nprintf "should-not-run-go:%s\\n" "$*"\n', 'utf-8');
        chmodSync(goBinaryPath, 0o755);
      }

      const result = spawnSync(process.execPath, [join(distCliDir, 'nana.js'), 'review-rules', 'scan'], {
        cwd: root,
        encoding: 'utf-8',
        env: {
          ...process.env,
          NANA_GO_SHIM_ACTIVE: '1',
        },
      });

      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /shim-fallback:review-rules scan/);
      assert.doesNotMatch(result.stdout, /should-not-run-go:/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('prefers the Go binary when bin/nana exists beside the wrapper tree', () => {
    const root = mkdtempSync(join(tmpdir(), 'nana-wrapper-go-'));
    try {
      const distCliDir = join(root, 'dist', 'cli');
      const distUtilsDir = join(root, 'dist', 'utils');
      const binDir = join(root, 'bin');
      mkdirSync(distCliDir, { recursive: true });
      mkdirSync(distUtilsDir, { recursive: true });
      mkdirSync(binDir, { recursive: true });

      const sourceWrapper = join(process.cwd(), 'dist', 'cli', 'nana.js');
      const sourceIndex = join(process.cwd(), 'dist', 'cli', 'index.js');
      const sourceNativeAssets = join(process.cwd(), 'dist', 'cli', 'native-assets.js');
      const sourcePlatformCommand = join(process.cwd(), 'dist', 'utils', 'platform-command.js');
      const sourcePackageUtil = join(process.cwd(), 'dist', 'utils', 'package.js');
      copyFileSync(sourceWrapper, join(distCliDir, 'nana.js'));
      copyFileSync(sourceIndex, join(distCliDir, 'index.js'));
      copyFileSync(sourceNativeAssets, join(distCliDir, 'native-assets.js'));
      copyFileSync(sourcePlatformCommand, join(distUtilsDir, 'platform-command.js'));
      copyFileSync(sourcePackageUtil, join(distUtilsDir, 'package.js'));

      const goBinaryPath = join(binDir, process.platform === 'win32' ? 'nana.exe' : 'nana');
      if (process.platform === 'win32') {
        writeFileSync(
          goBinaryPath,
          [
            '@echo off',
            'echo preferred-go-wrapper:%*',
          ].join('\r\n'),
          'utf-8',
        );
      } else {
        writeFileSync(
          goBinaryPath,
          [
            '#!/bin/sh',
            'printf "preferred-go-wrapper:%s\\n" "$*"',
          ].join('\n'),
          'utf-8',
        );
        chmodSync(goBinaryPath, 0o755);
      }

      const result = spawnSync(process.execPath, [join(distCliDir, 'nana.js'), 'version'], {
        cwd: root,
        encoding: 'utf-8',
      });

      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /preferred-go-wrapper:version/);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('hydrates and prefers the Go binary when local bin/nana is absent', async () => {
    const root = mkdtempSync(join(tmpdir(), 'nana-wrapper-hydrated-'));
    const serverRoot = mkdtempSync(join(tmpdir(), 'nana-wrapper-assets-'));
    try {
      const distCliDir = join(root, 'dist', 'cli');
      const distUtilsDir = join(root, 'dist', 'utils');
      mkdirSync(distCliDir, { recursive: true });
      mkdirSync(distUtilsDir, { recursive: true });
      copyFileSync(join(process.cwd(), 'dist', 'cli', 'nana.js'), join(distCliDir, 'nana.js'));
      copyFileSync(join(process.cwd(), 'dist', 'cli', 'index.js'), join(distCliDir, 'index.js'));
      copyFileSync(join(process.cwd(), 'dist', 'cli', 'native-assets.js'), join(distCliDir, 'native-assets.js'));
      copyFileSync(join(process.cwd(), 'dist', 'utils', 'platform-command.js'), join(distUtilsDir, 'platform-command.js'));
      copyFileSync(join(process.cwd(), 'dist', 'utils', 'package.js'), join(distUtilsDir, 'package.js'));

      writeFileSync(join(root, 'package.json'), JSON.stringify({
        version: '0.11.12',
        repository: { url: 'git+https://github.com/Yeachan-Heo/nana.git' },
      }));

      const stagingDir = join(serverRoot, 'staging');
      mkdirSync(stagingDir, { recursive: true });
      const binaryName = process.platform === 'win32' ? 'nana.exe' : 'nana';
      const binaryPath = join(stagingDir, binaryName);
      if (process.platform === 'win32') {
        writeFileSync(binaryPath, '@echo off\r\necho hydrated-go-wrapper:%*\r\n', 'utf-8');
      } else {
        writeFileSync(binaryPath, '#!/bin/sh\nprintf "hydrated-go-wrapper:%s\\n" "$*"\n', 'utf-8');
        chmodSync(binaryPath, 0o755);
      }

      const archiveName = process.platform === 'win32' ? 'nana-x86_64-pc-windows-msvc.zip' : 'nana-x86_64-unknown-linux-musl.tar.gz';
      const archivePath = join(serverRoot, archiveName);
      const archive = process.platform === 'win32'
        ? spawnSync('powershell', ['-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-Command', `Compress-Archive -LiteralPath '${binaryPath.replace(/'/g, "''")}' -DestinationPath '${archivePath.replace(/'/g, "''")}' -Force`], { encoding: 'utf-8' })
        : spawnSync('tar', ['-czf', archivePath, '-C', stagingDir, binaryName], { encoding: 'utf-8' });
      assert.equal(archive.status, 0, archive.stderr || archive.stdout);

      const archiveBuffer = Buffer.from(readFileSync(archivePath));
      const manifest = {
        version: '0.11.12',
        tag: 'v0.11.12',
        assets: [
          {
            product: 'nana',
            version: '0.11.12',
            platform: process.platform === 'win32' ? 'win32' : 'linux',
            arch: 'x64',
            archive: archiveName,
            binary: binaryName,
            binary_path: binaryName,
            sha256: sha256(archiveBuffer),
            size: archiveBuffer.length,
            download_url: '',
          },
        ],
      };
      writeFileSync(join(serverRoot, 'native-release-manifest.json'), JSON.stringify(manifest, null, 2), 'utf-8');

      const server = createServer((req, res) => {
        const file = join(serverRoot, (req.url || '/').replace(/^\//, ''));
        try {
          const body = readFileSync(file);
          res.writeHead(200);
          res.end(body);
        } catch {
          res.writeHead(404);
          res.end('missing');
        }
      });
      await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
      const address = server.address();
      assert.ok(address && typeof address !== 'string');
      const baseUrl = `http://127.0.0.1:${address.port}`;
      manifest.assets[0].download_url = `${baseUrl}/${archiveName}`;
      writeFileSync(join(serverRoot, 'native-release-manifest.json'), JSON.stringify(manifest, null, 2), 'utf-8');

      const result = await new Promise<{ status: number | null; stdout: string; stderr: string }>((resolve, reject) => {
        const child = spawn(process.execPath, [join(distCliDir, 'nana.js'), 'version'], {
          cwd: root,
          env: {
            ...process.env,
            NANA_NATIVE_MANIFEST_URL: `${baseUrl}/native-release-manifest.json`,
            NANA_NATIVE_CACHE_DIR: join(root, '.cache'),
          },
          stdio: ['ignore', 'pipe', 'pipe'],
        });
        let stdout = '';
        let stderr = '';
        let settled = false;
        const finish = (value: { status: number | null; stdout: string; stderr: string }) => {
          if (settled) return;
          settled = true;
          clearTimeout(timeout);
          resolve(value);
        };
        const fail = (error: Error) => {
          if (settled) return;
          settled = true;
          clearTimeout(timeout);
          reject(error);
        };
        const timeout = setTimeout(() => {
          child.kill('SIGTERM');
          setTimeout(() => child.kill('SIGKILL'), 1000).unref();
          fail(new Error(`wrapper child timed out after ${wrapperTimeoutMs}ms`));
        }, wrapperTimeoutMs);
        child.stdout.setEncoding('utf-8');
        child.stderr.setEncoding('utf-8');
        child.stdout.on('data', (chunk) => { stdout += chunk; });
        child.stderr.on('data', (chunk) => { stderr += chunk; });
        child.on('error', fail);
        child.on('close', (status) => finish({ status, stdout, stderr }));
      });
      server.closeAllConnections?.();
      server.closeIdleConnections?.();
      await new Promise<void>((resolve, reject) => server.close((err) => err ? reject(err) : resolve()));

      assert.equal(result.status, 0, result.stderr || result.stdout);
      assert.match(result.stdout, /hydrated-go-wrapper:version/);
    } finally {
      rmSync(root, { recursive: true, force: true });
      rmSync(serverRoot, { recursive: true, force: true });
    }
  });
});

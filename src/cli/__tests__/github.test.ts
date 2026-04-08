import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { lstat, mkdir, mkdtemp, readFile, readlink, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, relative, resolve } from 'node:path';
import {
  DEFAULT_LANE_CONCERN_DESCRIPTORS,
  resolveConcernMatchForFiles,
  resolveGithubConcernRegistryDetails,
} from '../github-workon-concerns.js';
import { defaultUserCodexHome } from '../../utils/paths.js';
import {
  checkGithubWorkonRuntimeConsistency,
  continueGithubPublicationLoop,
  continueGithubSchedulerLoop,
  detectVerificationPlan,
  classifyGithubCiFailureEvidence,
  buildLaneExecutionInstructions,
  GITHUB_APPEND_ENV,
  githubCommand,
  githubReviewRulesCommand,
  investigateGithubTarget,
  isSandboxLeaseStale,
  parseGithubArgs,
  parseGithubTargetUrl,
  resolveGithubToken,
  writeVerificationScripts,
} from '../github.js';

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  const headers = new Headers(init.headers);
  if (!headers.has('content-type')) headers.set('content-type', 'application/json');
  return new Response(JSON.stringify(body), { ...init, headers });
}

function createFetchStub(routes: Record<string, unknown>): typeof fetch {
  return async (input) => {
    const url = typeof input === 'string'
      ? new URL(input)
      : input instanceof URL
        ? input
        : new URL(input.url);
    const key = `${url.pathname}${url.search}`;
    if (!(key in routes)) {
      return new Response(`unexpected route: ${key}`, { status: 500 });
    }
    return jsonResponse(routes[key]);
  };
}

function initGitRepo(path: string): string {
  execFileSync('git', ['init', '-b', 'main'], { cwd: path, stdio: 'ignore' });
  execFileSync('git', ['config', 'user.email', 'test@example.com'], { cwd: path, stdio: 'ignore' });
  execFileSync('git', ['config', 'user.name', 'Test User'], { cwd: path, stdio: 'ignore' });
  execFileSync('git', ['add', '--all'], { cwd: path, stdio: 'ignore' });
  execFileSync('git', ['commit', '--allow-empty', '-m', 'init'], { cwd: path, stdio: 'ignore' });
  return execFileSync('git', ['rev-parse', 'HEAD'], { cwd: path, encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
}

async function createBareRemote(root: string): Promise<{ barePath: string; mainSha: string; prSha: string }> {
  const seed = join(root, 'seed');
  const barePath = join(root, 'remote.git');
  await mkdir(seed, { recursive: true });
  await writeFile(join(seed, 'README.md'), '# widget\n', 'utf-8');
  const mainSha = initGitRepo(seed);

  execFileSync('git', ['checkout', '-b', 'feature/pr-77'], { cwd: seed, stdio: 'ignore' });
  await writeFile(join(seed, 'feature.txt'), 'feature branch\n', 'utf-8');
  execFileSync('git', ['add', 'feature.txt'], { cwd: seed, stdio: 'ignore' });
  execFileSync('git', ['commit', '-m', 'feature'], { cwd: seed, stdio: 'ignore' });
  const prSha = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: seed, encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();

  execFileSync('git', ['checkout', 'main'], { cwd: seed, stdio: 'ignore' });
  execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
  execFileSync('git', ['--git-dir', barePath, 'update-ref', 'refs/pull/77/head', prSha], { stdio: 'ignore' });

  return { barePath, mainSha, prSha };
}

describe('github work-on concerns', () => {
  it('uses highest-precedence repo concern override file and reports malformed overrides', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-concern-registry-'));
    try {
      await mkdir(join(wd, '.github'), { recursive: true });
      await mkdir(join(wd, '.nana'), { recursive: true });
      await writeFile(
        join(wd, '.github', 'nana-work-on-concerns.json'),
        JSON.stringify({
          version: 1,
          lanes: {
            'security-reviewer': {
              pathPrefixes: ['security-policies/'],
            },
          },
        }, null, 2),
        'utf-8',
      );
      await writeFile(
        join(wd, '.nana', 'work-on-concerns.json'),
        '{"version":1,"lanes":',
        'utf-8',
      );

      const details = await resolveGithubConcernRegistryDetails(wd);

      assert.equal(details.descriptor_sources['security-reviewer']?.source, 'repo_override');
      assert.equal(details.descriptor_sources['security-reviewer']?.path, join(wd, '.github', 'nana-work-on-concerns.json'));
      assert.equal(details.registry['security-reviewer']?.pathPrefixes?.includes('security-policies/'), true);
      assert.equal(details.diagnostics.length, 1);
      assert.equal(details.diagnostics[0]?.code, 'parse_error');
      assert.equal(details.diagnostics[0]?.path, join(wd, '.nana', 'work-on-concerns.json'));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('matches every hardening lane against a representative positive file', () => {
    const fixtures: Record<string, { file: string; expectedKind: 'direct' | 'fallback' | 'unknown' }> = {
      'perf-coder': { file: 'runtime/cache-hot-path.ts', expectedKind: 'direct' },
      'perf-reviewer': { file: 'benchmarks/query-benchmark.ts', expectedKind: 'direct' },
      'api-reviewer': { file: 'docs/openapi/service.yaml', expectedKind: 'direct' },
      'security-reviewer': { file: 'src/rbac-permissions.ts', expectedKind: 'fallback' },
      'dependency-expert': { file: 'package.json', expectedKind: 'direct' },
      'style-reviewer': { file: 'misc/notes.txt', expectedKind: 'direct' },
      'test-engineer': { file: 'src/__tests__/runtime.test.ts', expectedKind: 'direct' },
    };

    for (const [alias, fixture] of Object.entries(fixtures)) {
      const descriptor = DEFAULT_LANE_CONCERN_DESCRIPTORS[alias];
      const result = resolveConcernMatchForFiles({
        descriptor,
        descriptorSource: 'default',
        registry: DEFAULT_LANE_CONCERN_DESCRIPTORS,
        changedFiles: [fixture.file],
      });
      assert.deepEqual(result.matched_files, [fixture.file], `${alias} should match its representative file`);
      assert.equal(result.reasons[0]?.kind, fixture.expectedKind, `${alias} should record the expected match kind`);
      assert.equal(result.reasons[0]?.rule_source, 'default', `${alias} should report default rule source`);
    }
  });

  it('does not match unrelated files for each hardening lane', () => {
    const fixtures: Record<string, string> = {
      'perf-coder': 'docs/openapi/service.yaml',
      'perf-reviewer': 'auth/issuer-config.yaml',
      'api-reviewer': 'benchmarks/query-benchmark.ts',
      'security-reviewer': 'docs/openapi/service.yaml',
      'dependency-expert': 'src/__tests__/runtime.test.ts',
      'test-engineer': 'package.json',
    };

    for (const [alias, file] of Object.entries(fixtures)) {
      const descriptor = DEFAULT_LANE_CONCERN_DESCRIPTORS[alias];
      const result = resolveConcernMatchForFiles({
        descriptor,
        descriptorSource: 'default',
        registry: DEFAULT_LANE_CONCERN_DESCRIPTORS,
        changedFiles: [file],
      });
      assert.deepEqual(result.matched_files, [], `${alias} should not match unrelated file ${file}`);
    }
  });
});

describe('github work-on runtime consistency', () => {
  it('detects scheduler pass continuity gaps and malformed concern override files', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-runtime-consistency-'));
    const runDir = join(wd, 'runs', 'gh-gap-1');
    const laneRuntimeDir = join(runDir, 'lane-runtime');
    const repoCheckoutPath = join(wd, 'repo');
    try {
      await mkdir(laneRuntimeDir, { recursive: true });
      await mkdir(join(laneRuntimeDir, 'scheduler-passes'), { recursive: true });
      await mkdir(join(repoCheckoutPath, '.nana'), { recursive: true });
      await writeFile(join(repoCheckoutPath, '.nana', 'work-on-concerns.json'), '{"version":1,"lanes":', 'utf-8');
      await writeFile(join(laneRuntimeDir, 'event-counter.json'), JSON.stringify({ next_id: 1 }, null, 2), 'utf-8');
      await writeFile(join(laneRuntimeDir, 'scheduler-state.json'), JSON.stringify({
        version: 1,
        run_id: 'gh-gap-1',
        last_processed_event_id: 0,
        pass_count: 2,
        startup_pass_count: 1,
        watch_pass_count: 0,
        poll_pass_count: 1,
        watch_mode: 'poll-only',
        last_completed_pass_id: 3,
        last_pass_at: '2026-04-03T10:00:00.000Z',
      }, null, 2), 'utf-8');
      await writeFile(join(laneRuntimeDir, 'scheduler-passes', 'pass-0001.json'), JSON.stringify({
        version: 1,
        run_id: 'gh-gap-1',
        pass_id: 1,
        wake_reason: 'startup',
        watch_mode: 'poll-only',
        started_at: '2026-04-03T10:00:00.000Z',
        completed_at: '2026-04-03T10:00:01.000Z',
        last_processed_event_id_before: 0,
        last_processed_event_id_after: 0,
        replayed_event_count: 0,
        launched_lanes: [],
        invalidated_lanes: [],
        retried_lanes: [],
        recovery_events: [],
      }, null, 2), 'utf-8');
      await writeFile(join(laneRuntimeDir, 'scheduler-passes', 'pass-0003.json'), JSON.stringify({
        version: 1,
        run_id: 'gh-gap-1',
        pass_id: 3,
        wake_reason: 'poll',
        watch_mode: 'poll-only',
        started_at: '2026-04-03T10:05:00.000Z',
        completed_at: '2026-04-03T10:05:01.000Z',
        last_processed_event_id_before: 0,
        last_processed_event_id_after: 0,
        replayed_event_count: 0,
        launched_lanes: [],
        invalidated_lanes: [],
        retried_lanes: [],
        recovery_events: [],
      }, null, 2), 'utf-8');

      const report = await checkGithubWorkonRuntimeConsistency({
        manifest: {
          version: 3,
          run_id: 'gh-gap-1',
          created_at: '2026-04-03T10:00:00.000Z',
          updated_at: '2026-04-03T10:05:01.000Z',
          repo_slug: 'acme/widget',
          repo_owner: 'acme',
          repo_name: 'widget',
          managed_repo_root: wd,
          source_path: repoCheckoutPath,
          sandbox_id: 'issue-42-pr-1',
          sandbox_path: wd,
          sandbox_repo_path: repoCheckoutPath,
          considerations_active: [],
          role_layout: 'split',
          consideration_pipeline: [],
          lane_prompt_artifacts: [],
          team_resolved_aliases: [],
          team_resolved_roles: [],
          create_pr_on_complete: false,
          target_kind: 'issue',
          target_number: 42,
          target_title: 'Implement queue healing',
          target_url: 'https://github.com/acme/widget/issues/42',
          target_state: 'open',
          review_reviewer: 'dkropachev',
          api_base_url: 'https://api.github.com',
          default_branch: 'main',
          last_seen_issue_comment_id: 0,
          last_seen_review_id: 0,
          last_seen_review_comment_id: 0,
        },
        runPaths: {
          runDir,
          manifestPath: join(runDir, 'manifest.json'),
          startInstructionsPath: join(runDir, 'start-instructions.md'),
          feedbackInstructionsPath: join(runDir, 'feedback-instructions.md'),
        },
      });

      assert.equal(report.ok, false);
      assert.match(report.errors.join('\n'), /gap before pass 3/i);
      assert.match(report.warnings.join('\n'), /concern override diagnostic/i);
      assert.equal(report.stats.scheduler_pass_artifacts, 2);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });
});

describe('parseGithubTargetUrl', () => {
  it('parses issue URLs', () => {
    assert.deepEqual(
      parseGithubTargetUrl('https://github.com/dkropachev/alternator-client-java/issues/1'),
      {
        owner: 'dkropachev',
        repoName: 'alternator-client-java',
        repoSlug: 'dkropachev/alternator-client-java',
        targetKind: 'issue',
        targetNumber: 1,
        canonicalUrl: 'https://github.com/dkropachev/alternator-client-java/issues/1',
      },
    );
  });

  it('parses pull request URLs', () => {
    assert.equal(
      parseGithubTargetUrl('https://github.com/acme/widget/pull/77').targetKind,
      'pr',
    );
  });
});

describe('parseGithubArgs', () => {
  it('parses the simplified URL-first start form', () => {
    assert.deepEqual(
      parseGithubArgs(['start', 'https://github.com/acme/widget/issues/42', '--reviewer', '@me', '--', '--model', 'gpt-5.4']),
      {
        subcommand: 'start',
        target: {
          owner: 'acme',
          repoName: 'widget',
          repoSlug: 'acme/widget',
          targetKind: 'issue',
          targetNumber: 42,
          canonicalUrl: 'https://github.com/acme/widget/issues/42',
        },
        reviewer: '@me',
        requestedConsiderations: [],
        roleLayout: undefined,
        newPr: false,
        createPr: false,
        codexArgs: ['--model', 'gpt-5.4'],
      },
    );
  });

  it('parses requested considerations', () => {
    assert.deepEqual(
      parseGithubArgs([
        'start',
        'https://github.com/acme/widget/issues/42',
        '--considerations',
        'arch,perf,api',
      ]),
      {
        subcommand: 'start',
        target: {
          owner: 'acme',
          repoName: 'widget',
          repoSlug: 'acme/widget',
          targetKind: 'issue',
          targetNumber: 42,
          canonicalUrl: 'https://github.com/acme/widget/issues/42',
        },
        reviewer: '@me',
        requestedConsiderations: ['arch', 'perf', 'api'],
        roleLayout: undefined,
        newPr: false,
        createPr: false,
        codexArgs: [],
      },
    );
  });

  it('parses reviewer+executor role layout', () => {
    assert.deepEqual(
      parseGithubArgs([
        'start',
        'https://github.com/acme/widget/issues/42',
        '--considerations',
        'security,api',
        '--role-layout',
        'reviewer+executor',
      ]),
      {
        subcommand: 'start',
        target: {
          owner: 'acme',
          repoName: 'widget',
          repoSlug: 'acme/widget',
          targetKind: 'issue',
          targetNumber: 42,
          canonicalUrl: 'https://github.com/acme/widget/issues/42',
        },
        reviewer: '@me',
        requestedConsiderations: ['security', 'api'],
        roleLayout: 'reviewer+executor',
        newPr: false,
        createPr: false,
        codexArgs: [],
      },
    );
  });

  it('keeps sync defaulting to the latest run', () => {
    assert.deepEqual(
      parseGithubArgs(['sync', '--resume-last']),
      {
        subcommand: 'sync',
        runId: undefined,
        useLastRun: true,
        reviewer: undefined,
        resumeLast: true,
        feedbackTargetUrl: undefined,
        codexArgs: [],
      },
    );
  });

  it('parses sync with a GitHub target URL override', () => {
    assert.deepEqual(
      parseGithubArgs(['sync', '--run-id', 'gh-123', 'https://github.com/acme/widget/pull/6']),
      {
        subcommand: 'sync',
        runId: 'gh-123',
        useLastRun: false,
        reviewer: undefined,
        resumeLast: false,
        feedbackTargetUrl: 'https://github.com/acme/widget/pull/6',
        codexArgs: [],
      },
    );
  });

  it('falls back to `gh auth token` when env tokens are missing', () => {
    const token = resolveGithubToken(
      {} as NodeJS.ProcessEnv,
      'https://api.github.com',
      ((command: string, args: readonly string[]) => {
        assert.equal(command, 'gh');
        assert.deepEqual(args, ['auth', 'token']);
        return 'gh-cli-token\n';
      }) as typeof execFileSync,
    );

    assert.equal(token, 'gh-cli-token');
  });

  it('parses defaults set', () => {
    assert.deepEqual(
      parseGithubArgs(['defaults', 'set', 'acme/widget', '--considerations', 'arch,perf']),
      {
        subcommand: 'defaults-set',
        repoSlug: 'acme/widget',
        considerations: ['arch', 'perf'],
        roleLayout: undefined,
        reviewRulesMode: undefined,
        reviewRulesTrustedReviewers: undefined,
        reviewRulesBlockedReviewers: undefined,
        reviewRulesMinDistinctReviewers: undefined,
      },
    );
  });

  it('parses defaults set role layout', () => {
    assert.deepEqual(
      parseGithubArgs(['defaults', 'set', 'acme/widget', '--role-layout', 'reviewer+executor']),
      {
        subcommand: 'defaults-set',
        repoSlug: 'acme/widget',
        considerations: [],
        roleLayout: 'reviewer+executor',
        reviewRulesMode: undefined,
        reviewRulesTrustedReviewers: undefined,
        reviewRulesBlockedReviewers: undefined,
        reviewRulesMinDistinctReviewers: undefined,
      },
    );
  });

  it('parses defaults set review-rules mode', () => {
    assert.deepEqual(
      parseGithubArgs(['defaults', 'set', 'acme/widget', '--review-rules-mode', 'automatic']),
      {
        subcommand: 'defaults-set',
        repoSlug: 'acme/widget',
        considerations: [],
        roleLayout: undefined,
        reviewRulesMode: 'automatic',
        reviewRulesTrustedReviewers: undefined,
        reviewRulesBlockedReviewers: undefined,
        reviewRulesMinDistinctReviewers: undefined,
      },
    );
  });

  it('parses stats for a GitHub target URL', () => {
    assert.deepEqual(
      parseGithubArgs(['stats', 'https://github.com/acme/widget/issues/42']),
      {
        subcommand: 'stats',
        target: {
          owner: 'acme',
          repoName: 'widget',
          repoSlug: 'acme/widget',
          targetKind: 'issue',
          targetNumber: 42,
          canonicalUrl: 'https://github.com/acme/widget/issues/42',
        },
      },
    );
  });

  it('parses retrospective run lookup arguments', () => {
    assert.deepEqual(
      parseGithubArgs(['retrospective', '--run-id', 'gh-123']),
      {
        subcommand: 'retrospective',
        runId: 'gh-123',
        useLastRun: false,
      },
    );
  });
});

describe('isSandboxLeaseStale', () => {
  it('treats expired leases as stale', () => {
    assert.equal(
      isSandboxLeaseStale({
        version: 1,
        sandbox_id: 'sb-001',
        owner_pid: process.pid,
        owner_run_id: 'run-1',
        target_url: 'https://github.com/acme/widget/issues/1',
        acquired_at: '2026-04-03T10:00:00.000Z',
        heartbeat_at: '2026-04-03T10:00:00.000Z',
        expires_at: '2026-04-03T10:00:01.000Z',
      }, Date.parse('2026-04-03T10:01:00.000Z')),
      true,
    );
  });
});

describe('classifyGithubCiFailureEvidence', () => {
  it('classifies infrastructure/network failures as environmental', () => {
    const result = classifyGithubCiFailureEvidence(
      'Build and Test',
      'Error: connection timed out while downloading dependencies\nrunner has received a shutdown signal\n',
    );
    assert.equal(result.category, 'environmental');
    assert.ok(result.evidence.length > 0);
  });

  it('classifies compile/test failures as code-caused', () => {
    const result = classifyGithubCiFailureEvidence(
      'Build and Test',
      'COMPILATION ERROR :\ncannot find symbol\n',
    );
    assert.equal(result.category, 'code');
  });
});

describe('verification plan', () => {
  it('detects lint, compile, and unit commands from workflow + Makefile', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-verification-plan-'));
    try {
      await mkdir(join(wd, '.github', 'workflows'), { recursive: true });
      await writeFile(join(wd, 'Makefile'), [
        'lint:',
        '\techo lint',
        'compile:',
        '\techo compile',
        'compile-test:',
        '\techo compile-test',
        'compile-demo:',
        '\techo compile-demo',
        'test-unit:',
        '\techo test-unit',
        'test-integration:',
        '\techo test-integration',
        '',
      ].join('\n'));
      await writeFile(join(wd, '.github', 'workflows', 'continuous-integration.yml'), [
        'jobs:',
        '  lint:',
        '    steps:',
        '      - run: make lint',
        '  build:',
        '    steps:',
        '      - run: make compile',
        '      - run: make compile-test',
        '      - run: make compile-demo',
        '      - run: make test-unit',
        '      - run: make test-integration',
        '',
      ].join('\n'));

      const plan = await detectVerificationPlan(wd);
      assert.equal(plan.source, 'workflow');
      assert.deepEqual(plan.lint, ['make lint']);
      assert.deepEqual(plan.compile, ['make compile', 'make compile-test', 'make compile-demo']);
      assert.deepEqual(plan.unit, ['make test-unit']);
      assert.deepEqual(plan.integration, ['make test-integration']);
      assert.match(plan.plan_fingerprint, /^[a-f0-9]{64}$/);
      assert.equal(plan.source_files.length, 2);
      assert.deepEqual(
        plan.source_files.map((file) => ({ path: file.path, kind: file.kind })),
        [
          { path: 'Makefile', kind: 'makefile' },
          { path: '.github/workflows/continuous-integration.yml', kind: 'workflow' },
        ],
      );
      for (const sourceFile of plan.source_files) {
        assert.match(sourceFile.checksum, /^[a-f0-9]{64}$/);
      }
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('tracks wrapper scripts referenced from workflow commands', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-verification-wrapper-'));
    try {
      await mkdir(join(wd, '.github', 'workflows'), { recursive: true });
      await mkdir(join(wd, 'scripts'), { recursive: true });
      await writeFile(join(wd, 'Makefile'), [
        'lint:',
        '\techo lint',
        'test-unit:',
        '\techo test-unit',
        '',
      ].join('\n'));
      await writeFile(join(wd, 'scripts', 'ci.sh'), [
        '#!/usr/bin/env bash',
        'make lint',
        'make test-unit',
        '',
      ].join('\n'));
      await writeFile(join(wd, '.github', 'workflows', 'continuous-integration.yml'), [
        'jobs:',
        '  verify:',
        '    steps:',
        '      - run: bash ./scripts/ci.sh',
        '',
      ].join('\n'));

      const plan = await detectVerificationPlan(wd);
      assert.equal(plan.source, 'workflow');
      assert.deepEqual(plan.lint, ['make lint']);
      assert.deepEqual(plan.compile, []);
      assert.deepEqual(plan.unit, ['make test-unit']);
      assert.deepEqual(
        plan.source_files.map((file) => ({ path: file.path, kind: file.kind })),
        [
          { path: 'Makefile', kind: 'makefile' },
          { path: 'scripts/ci.sh', kind: 'script' },
          { path: '.github/workflows/continuous-integration.yml', kind: 'workflow' },
        ],
      );
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('writes worker and full verification scripts for the detected plan', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-verification-scripts-'));
    try {
      const sandboxPath = join(wd, 'sandbox');
      const repoCheckoutPath = join(sandboxPath, 'repo');
      await mkdir(repoCheckoutPath, { recursive: true });
      const managedRepoRoot = join(wd, 'managed-repo');
      await mkdir(managedRepoRoot, { recursive: true });

      const dir = await writeVerificationScripts(
        sandboxPath,
        repoCheckoutPath,
        {
          source: 'workflow',
          lint: ['make lint'],
          compile: ['make compile', 'make compile-test'],
          unit: ['make test-unit'],
          integration: ['make test-integration'],
          plan_fingerprint: 'deadbeef',
          source_files: [
            {
              path: '.github/workflows/continuous-integration.yml',
              checksum: 'abc123',
              kind: 'workflow',
            },
          ],
        },
        'gh-run-123',
        {
          managedRepoRoot,
          sandboxId: 'issue-42-pr-run',
          prMode: true,
          env: { ...process.env } as NodeJS.ProcessEnv,
        },
      );

      const allScript = await readFile(join(dir, 'all.sh'), 'utf-8');
      const workerDoneScript = await readFile(join(dir, 'worker-done.sh'), 'utf-8');
      const refreshScript = await readFile(join(dir, 'refresh.sh'), 'utf-8');
      const policyEnv = await readFile(join(dir, 'unit-policy.env'), 'utf-8');
      const integrationPolicyEnv = await readFile(join(dir, 'integration-policy.env'), 'utf-8');
      const planJson = JSON.parse(await readFile(join(dir, 'plan.json'), 'utf-8')) as {
        source: string;
        lint: string[];
        compile: string[];
        unit: string[];
        integration: string[];
        plan_fingerprint: string;
        source_files: Array<{ path: string; checksum: string; kind: string }>;
        unit_policy: { mode: string; sample_count: number };
        integration_policy: { mode: string; sample_count: number };
        every_iteration_threshold_ms: number;
        ci_only_threshold_ms: number;
        scripts: { refresh: string; unit_policy: string; integration_policy: string };
      };

      assert.match(allScript, /refresh\.sh/);
      assert.match(allScript, /lint\.sh/);
      assert.match(allScript, /compile\.sh/);
      assert.match(allScript, /unit-tests\.sh/);
      assert.match(allScript, /integration-tests\.sh/);
      assert.match(workerDoneScript, /refresh\.sh/);
      assert.match(workerDoneScript, /UNIT_POLICY_FILE=/);
      assert.match(workerDoneScript, /INTEGRATION_POLICY_FILE=/);
      assert.match(workerDoneScript, /REPO_HISTORY_FILE=/);
      assert.match(workerDoneScript, /PLAN_FINGERPRINT='deadbeef'|PLAN_FINGERPRINT=deadbeef/);
      assert.match(workerDoneScript, /unit-tests\.sh/);
      assert.match(workerDoneScript, /Skipping unit tests on worker completion because mode=/i);
      assert.match(refreshScript, /(?:nana work-on|nana\.js' work-on) verify-refresh --run-id /);
      assert.match(refreshScript, /gh-run-123/);
      assert.match(policyEnv, /NANA_WORKON_UNIT_TESTS_MODE=unknown/);
      assert.match(integrationPolicyEnv, /NANA_WORKON_INTEGRATION_TESTS_MODE=final-only/);
      assert.equal(planJson.source, 'workflow');
      assert.deepEqual(planJson.lint, ['make lint']);
      assert.deepEqual(planJson.compile, ['make compile', 'make compile-test']);
      assert.deepEqual(planJson.unit, ['make test-unit']);
      assert.deepEqual(planJson.integration, ['make test-integration']);
      assert.equal(planJson.plan_fingerprint, 'deadbeef');
      assert.deepEqual(planJson.source_files, [{ path: '.github/workflows/continuous-integration.yml', checksum: 'abc123', kind: 'workflow' }]);
      assert.equal(planJson.unit_policy.mode, 'unknown');
      assert.equal(planJson.unit_policy.sample_count, 0);
      assert.equal(planJson.integration_policy.mode, 'final-only');
      assert.equal(planJson.every_iteration_threshold_ms, 180000);
      assert.equal(planJson.ci_only_threshold_ms, 900000);
      assert.ok(planJson.scripts.refresh.endsWith('/refresh.sh'));
      assert.ok(planJson.scripts.unit_policy.endsWith('/unit-policy.env'));
      assert.ok(planJson.scripts.integration_policy.endsWith('/integration-policy.env'));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });
});

describe('githubCommand', () => {
  it('investigates an issue without creating a work-on run', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-investigate-readonly-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const { barePath } = await createBareRemote(wd);
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const lines: string[] = [];
      await investigateGithubTarget('https://github.com/acme/widget/issues/42', {
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        fetchImpl: createFetchStub({
          '/user': { login: 'dkropachev' },
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/issues/42': {
            number: 42,
            title: 'Implement queue healing',
            body: 'Need to keep workers alive after review updates.',
            html_url: 'https://github.com/acme/widget/issues/42',
            state: 'open',
            updated_at: '2026-04-03T09:00:00.000Z',
            user: { login: 'requester' },
          },
          '/repos/acme/widget/issues/42/comments?per_page=100': [],
        }),
        writeLine: (line) => lines.push(line),
      });

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      assert.equal(existsSync(join(managedRepoRoot, 'latest-run.json')), false);
      assert.match(lines.join('\n'), /Investigated acme\/widget issue #42/i);
      assert.match(lines.join('\n'), /Review-rules mode: manual/i);
      assert.match(lines.join('\n'), /Next: nana implement https:\/\/github.com\/acme\/widget\/issues\/42/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('scans PR review history into pending repo rules, approves them, and injects approved rules into work-on instructions', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-review-rules');
      const barePath = join(wd, 'review-rules-remote.git');
      await mkdir(join(seed, 'src', 'api'), { recursive: true });
      await writeFile(join(seed, 'README.md'), '# widget\n', 'utf-8');
      await writeFile(join(seed, 'src', 'api', 'client.ts'), [
        'export function searchDocuments(query: string): string {',
        '  return query.trim();',
        '}',
        '',
        'export function updateQueueState(state: string): string {',
        '  return state.toLowerCase();',
        '}',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const reviewRoutes = {
        '/repos/acme/widget': {
          name: 'widget',
          full_name: repoSlug,
          clone_url: barePath,
          default_branch: 'main',
          html_url: 'https://github.com/acme/widget',
        },
        '/repos/acme/widget/pulls?state=all&per_page=100&page=1': [
          {
            number: 7,
            title: 'Add queue recovery',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'closed',
            updated_at: '2026-04-02T10:00:00.000Z',
            head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
          {
            number: 8,
            title: 'Tighten API handling',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/8',
            state: 'closed',
            updated_at: '2026-04-02T11:00:00.000Z',
            head: { ref: 'feature/pr-8', sha: 'sha-pr-8', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
        ],
        '/repos/acme/widget/pulls?state=all&per_page=100&page=2': [],
        '/repos/acme/widget/pulls/7': {
          number: 7,
          title: 'Add queue recovery',
          body: null,
          html_url: 'https://github.com/acme/widget/pull/7',
          state: 'closed',
          updated_at: '2026-04-02T10:00:00.000Z',
          head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
          base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
        },
        '/repos/acme/widget/pulls/7/reviews?per_page=100': [
          {
            id: 701,
            html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-701',
            body: 'Please add regression tests for this behavior change before merge.',
            submitted_at: '2026-04-02T12:00:00.000Z',
            state: 'CHANGES_REQUESTED',
            user: { login: 'reviewer-a' },
          },
        ],
        '/repos/acme/widget/pulls/7/comments?per_page=100': [
          {
            id: 801,
            html_url: 'https://github.com/acme/widget/pull/7#discussion_r801',
            body: 'We should keep the public API backward compatible here.',
            created_at: '2026-04-02T12:05:00.000Z',
            updated_at: '2026-04-02T12:05:00.000Z',
            path: 'src/api/client.ts',
            line: 14,
            diff_hunk: '@@ -1,3 +1,3 @@',
            user: { login: 'reviewer-a' },
            pull_request_review_id: 701,
          },
        ],
        '/repos/acme/widget/pulls/8': {
          number: 8,
          title: 'Tighten API handling',
          body: null,
          html_url: 'https://github.com/acme/widget/pull/8',
          state: 'closed',
          updated_at: '2026-04-02T11:00:00.000Z',
          head: { ref: 'feature/pr-8', sha: 'sha-pr-8', repo: { full_name: repoSlug } },
          base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
        },
        '/repos/acme/widget/pulls/8/reviews?per_page=100': [
          {
            id: 702,
            html_url: 'https://github.com/acme/widget/pull/8#pullrequestreview-702',
            body: 'Needs regression coverage before we merge this.',
            submitted_at: '2026-04-02T13:00:00.000Z',
            state: 'COMMENTED',
            user: { login: 'reviewer-b' },
          },
        ],
        '/repos/acme/widget/pulls/8/comments?per_page=100': [
          {
            id: 802,
            html_url: 'https://github.com/acme/widget/pull/8#discussion_r802',
            body: 'Avoid breaking the public API contract for callers.',
            created_at: '2026-04-02T13:05:00.000Z',
            updated_at: '2026-04-02T13:05:00.000Z',
            path: 'src/api/client.ts',
            line: 1,
            diff_hunk: '@@ -10,3 +10,3 @@',
            user: { login: 'reviewer-b' },
            pull_request_review_id: 702,
          },
          {
            id: 803,
            html_url: 'https://github.com/acme/widget/pull/8#discussion_r803',
            body: 'Variable naming here is odd.',
            created_at: '2026-04-02T13:06:00.000Z',
            updated_at: '2026-04-02T13:06:00.000Z',
            path: 'src/api/client.ts',
            line: 5,
            diff_hunk: '@@ -4,3 +4,3 @@',
            user: { login: 'reviewer-c' },
            pull_request_review_id: 702,
          },
        ],
        '/repos/acme/widget/contents/src/api/client.ts?ref=sha-pr-8': {
          content: Buffer.from([
            'export function searchDocuments(query: string): string {',
            '  return query.trim();',
            '}',
            '',
            'export function updateQueueState(state: string): string {',
            '  return state.toLowerCase();',
            '}',
            '',
          ].join('\n'), 'utf-8').toString('base64'),
          encoding: 'base64',
        },
      } satisfies Record<string, unknown>;

      const scanOutput: string[] = [];
      await githubReviewRulesCommand(['scan', 'acme/widget'], {
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(reviewRoutes),
        writeLine: (line) => scanOutput.push(line),
      });

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const scannedRules = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        pending_candidates: Array<{
          id: string;
          category: string;
          rule: string;
          extraction_origin: string;
          extraction_reason: string;
          evidence: Array<{ code_context_excerpt?: string; path?: string; code_context_provenance?: string; code_context_ref?: string }>;
        }>;
        approved_rules: Array<{ id: string }>;
      };
      assert.equal(scannedRules.approved_rules.length, 0);
      assert.equal(scannedRules.pending_candidates.some((rule) => rule.category === 'qa'), true);
      assert.equal(scannedRules.pending_candidates.some((rule) => rule.category === 'api'), true);
      assert.equal(scannedRules.pending_candidates.some((rule) => rule.category === 'style'), false);
      assert.equal(
        scannedRules.pending_candidates.some((rule) => rule.evidence.some((evidence) => evidence.path === 'src/api/client.ts' && /1: export function searchDocuments/i.test(evidence.code_context_excerpt ?? ''))),
        true,
      );
      const apiRule = scannedRules.pending_candidates.find((rule) => rule.category === 'api');
      assert.equal(apiRule?.extraction_origin, 'review_comments');
      assert.match(apiRule?.extraction_reason ?? '', /Repeated review comments across 2 PRs?/i);
      assert.equal(apiRule?.evidence.some((evidence) => evidence.code_context_provenance === 'pr_head_sha' && evidence.code_context_ref === 'sha-pr-8'), true);
      assert.ok(scanOutput.some((line) => /pending .*Add or update targeted regression coverage/i.test(line)));
      assert.ok(scanOutput.some((line) => /origin=review_comments/i.test(line)));

      const qaRuleId = scannedRules.pending_candidates.find((rule) => rule.category === 'qa')?.id;
      const apiRuleId = scannedRules.pending_candidates.find((rule) => rule.category === 'api')?.id;
      assert.ok(qaRuleId);
      assert.ok(apiRuleId);

      const listOutput: string[] = [];
      await githubReviewRulesCommand(['list', 'acme/widget'], {
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(reviewRoutes),
        writeLine: (line) => listOutput.push(line),
      });
      assert.ok(listOutput.some((line) => /pending .*confidence=/i.test(line)));

      await githubReviewRulesCommand(['approve', 'acme/widget', qaRuleId!], {
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(reviewRoutes),
        writeLine: () => {},
      });

      const approvedRules = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        pending_candidates: Array<{ id: string; category: string }>;
        approved_rules: Array<{ id: string; category: string; rule: string }>;
      };
      assert.equal(approvedRules.pending_candidates.some((rule) => rule.category === 'api'), true);
      assert.equal(approvedRules.approved_rules.some((rule) => rule.category === 'qa'), true);
      assert.equal(approvedRules.approved_rules.some((rule) => rule.category === 'api'), false);

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'api,qa'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            ...reviewRoutes,
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
            '/repos/acme/widget/pulls?state=all&head=acme%3Anana%2Fissue-42%2Fissue-42-pr-177521040000&per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: () => {},
        },
      );

      const latest = JSON.parse(await readFile(join(homeDir, '.nana', 'repos', 'acme', 'widget', 'latest-run.json'), 'utf-8')) as { run_id: string };
      const manifest = JSON.parse(await readFile(join(homeDir, '.nana', 'repos', 'acme', 'widget', 'runs', latest.run_id, 'manifest.json'), 'utf-8'));
      const appendix = await readFile(join(homeDir, '.nana', 'repos', 'acme', 'widget', 'runs', latest.run_id, 'start-instructions.md'), 'utf-8');
      assert.match(appendix, /## Approved repo review rules/i);
      assert.match(appendix, /Add or update targeted regression coverage for behavior changes and bug fixes before considering the work complete\./i);
      assert.doesNotMatch(appendix, /Treat public APIs, schemas, and documented contracts as compatibility surfaces/i);

      await githubReviewRulesCommand(['approve', 'acme/widget', apiRuleId!], {
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(reviewRoutes),
        writeLine: () => {},
      });

      const finalRules = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        pending_candidates: Array<{ id: string }>;
        approved_rules: Array<{ id: string; category: string }>;
      };
      assert.equal(finalRules.pending_candidates.length, 0);
      assert.equal(finalRules.approved_rules.some((rule) => rule.category === 'api'), true);

      const apiLane = manifest.consideration_pipeline.find((lane: { alias: string }) => lane.alias === 'api-reviewer');
      const qaLane = manifest.consideration_pipeline.find((lane: { alias: string }) => lane.alias === 'test-engineer');
      assert.ok(apiLane);
      assert.ok(qaLane);
      const apiLaneInstructions = await buildLaneExecutionInstructions(manifest, apiLane, undefined);
      const qaLaneInstructions = await buildLaneExecutionInstructions(manifest, qaLane, undefined);
      assert.match(apiLaneInstructions, /Treat public APIs, schemas, and documented contracts as compatibility surfaces/i);
      assert.match(qaLaneInstructions, /Add or update targeted regression coverage for behavior changes and bug fixes before considering the work complete\./i);
      assert.doesNotMatch(qaLaneInstructions, /Treat public APIs, schemas, and documented contracts as compatibility surfaces/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('auto-approves extracted review rules during investigate when global mode is automatic', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-auto-global-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-auto-global');
      const barePath = join(wd, 'auto-global-remote.git');
      await mkdir(join(seed, 'src', 'api'), { recursive: true });
      await writeFile(join(seed, 'src', 'api', 'client.ts'), 'export function searchDocuments(query: string): string { return query.trim(); }\n', 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      const lines: string[] = [];
      await investigateGithubTarget('https://github.com/acme/widget/pull/7', {
        env,
        homeDir,
        fetchImpl: createFetchStub({
          '/user': { login: 'dkropachev' },
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/issues/7': {
            number: 7,
            title: 'Tighten API handling',
            body: 'Follow prior API review guidance.',
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'open',
            updated_at: '2026-04-03T09:00:00.000Z',
            user: { login: 'requester' },
            pull_request: {},
          },
          '/repos/acme/widget/pulls/7': {
            number: 7,
            title: 'Tighten API handling',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'open',
            updated_at: '2026-04-03T09:00:00.000Z',
            head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
          '/repos/acme/widget/contents/src/api/client.ts?ref=sha-pr-7': {
            content: Buffer.from('export function searchDocuments(query: string): string { return query.trim(); }\n', 'utf-8').toString('base64'),
            encoding: 'base64',
          },
          '/repos/acme/widget/pulls/7/reviews?per_page=100': [
            {
              id: 701,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-701',
              body: 'Please add regression tests for this behavior change before merge.',
              submitted_at: '2026-04-02T12:00:00.000Z',
              state: 'CHANGES_REQUESTED',
              user: { login: 'reviewer-a' },
            },
            {
              id: 702,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-702',
              body: 'Needs regression coverage before we merge this.',
              submitted_at: '2026-04-02T13:00:00.000Z',
              state: 'COMMENTED',
              user: { login: 'reviewer-b' },
            },
          ],
          '/repos/acme/widget/pulls/7/comments?per_page=100': [],
        }),
        writeLine: (line) => lines.push(line),
      });

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const rulesDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        approved_rules: Array<{ category: string }>;
        pending_candidates: Array<{ category: string }>;
      };
      assert.equal(rulesDoc.approved_rules.some((rule) => rule.category === 'qa'), true);
      assert.equal(rulesDoc.pending_candidates.length, 0);
      assert.ok(lines.some((line) => /Review-rules automatic refresh/i.test(line)));
      assert.ok(lines.some((line) => /Review-rules mode: automatic/i.test(line)));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('auto-approves extracted review rules during work-on start for PR targets when mode is automatic', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-auto-start-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const { barePath, prSha } = await createBareRemote(wd);
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      const lines: string[] = [];
      await githubCommand(
        ['start', 'https://github.com/acme/widget/pull/77'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: 'acme/widget',
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/77': {
              number: 77,
              title: 'PR review target',
              body: 'Drive rule extraction from PR start.',
              html_url: 'https://github.com/acme/widget/pull/77',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
              pull_request: {},
            },
            '/repos/acme/widget/pulls/77': {
              number: 77,
              title: 'PR review target',
              body: 'body',
              html_url: 'https://github.com/acme/widget/pull/77',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              head: { ref: 'feature/pr-77', sha: prSha, repo: { full_name: 'acme/widget' } },
              base: { ref: 'main', sha: 'base-sha', repo: { full_name: 'acme/widget' } },
            },
            '/repos/acme/widget/issues/77/comments?per_page=100': [],
            '/repos/acme/widget/pulls/77/reviews?per_page=100': [
              {
                id: 7701,
                html_url: 'https://github.com/acme/widget/pull/77#pullrequestreview-7701',
                body: 'Please add regression tests for this behavior change before merge.',
                submitted_at: '2026-04-02T12:00:00.000Z',
                state: 'CHANGES_REQUESTED',
                user: { login: 'reviewer-a' },
              },
              {
                id: 7702,
                html_url: 'https://github.com/acme/widget/pull/77#pullrequestreview-7702',
                body: 'Needs regression coverage before we merge this.',
                submitted_at: '2026-04-02T13:00:00.000Z',
                state: 'COMMENTED',
                user: { login: 'reviewer-b' },
              },
            ],
            '/repos/acme/widget/pulls/77/comments?per_page=100': [],
            [`/repos/acme/widget/commits/${encodeURIComponent(prSha)}/check-runs?per_page=100`]: {
              total_count: 1,
              check_runs: [{ id: 77, name: 'CI', status: 'completed', conclusion: 'success' }],
            },
            [`/repos/acme/widget/actions/runs?head_sha=${encodeURIComponent(prSha)}&per_page=100`]: {
              total_count: 1,
              workflow_runs: [{ id: 77, name: 'CI', head_sha: prSha, status: 'completed', conclusion: 'success', html_url: 'https://example.invalid/actions/77' }],
            },
          }),
          launchWithHud: async () => {},
          writeLine: (line) => lines.push(line),
        },
      );

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const rulesDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        approved_rules: Array<{ category: string }>;
      };
      assert.equal(rulesDoc.approved_rules.some((rule) => rule.category === 'qa'), true);
      assert.ok(lines.some((line) => /Review-rules automatic refresh/i.test(line)));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('shows global and repo review-rules mode via config show', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-config-show-'));
    const homeDir = join(wd, 'home');
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-config-show');
      const barePath = join(wd, 'config-show-remote.git');
      await mkdir(seed, { recursive: true });
      await writeFile(join(seed, 'README.md'), '# widget\n', 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env: { ...process.env } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      await githubCommand(['defaults', 'set', 'acme/widget', '--review-rules-mode', 'manual'], {
        env: { ...process.env } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      const lines: string[] = [];
      await githubReviewRulesCommand(['config', 'show', 'acme/widget'], {
        env: { ...process.env } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub({
          '/repos/acme/widget': {
            name: 'widget',
            full_name: 'acme/widget',
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
        }),
        writeLine: (line) => lines.push(line),
      });

      const output = lines.join('\n');
      assert.match(output, /Global review-rules mode: automatic/i);
      assert.match(output, /Repo review-rules mode for acme\/widget: manual/i);
      assert.match(output, /Effective review-rules mode for acme\/widget: manual/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('applies reviewer trust policy when extracting review rules', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-trust-policy-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-trust-policy');
      const barePath = join(wd, 'trust-policy-remote.git');
      await mkdir(join(seed, 'src', 'api'), { recursive: true });
      await writeFile(join(seed, 'src', 'api', 'client.ts'), 'export function searchDocuments(query: string): string { return query.trim(); }\n', 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubCommand(['defaults', 'set', 'acme/widget', '--review-rules-trusted-reviewers', 'reviewer-a,reviewer-b', '--review-rules-blocked-reviewers', 'reviewer-c', '--review-rules-min-distinct-reviewers', '2'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      const lines: string[] = [];
      await githubReviewRulesCommand(['scan', 'acme/widget'], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub({
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/pulls?state=all&per_page=100&page=1': [
            {
              number: 7,
              title: 'Review policy PR',
              body: null,
              html_url: 'https://github.com/acme/widget/pull/7',
              state: 'closed',
              updated_at: '2026-04-02T11:00:00.000Z',
              head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
              base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
            },
          ],
          '/repos/acme/widget/pulls?state=all&per_page=100&page=2': [],
          '/repos/acme/widget/pulls/7': {
            number: 7,
            title: 'Review policy PR',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'closed',
            updated_at: '2026-04-02T11:00:00.000Z',
            head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
          '/repos/acme/widget/pulls/7/reviews?per_page=100': [
            {
              id: 701,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-701',
              body: 'Please add regression tests for this behavior change before merge.',
              submitted_at: '2026-04-02T12:00:00.000Z',
              state: 'CHANGES_REQUESTED',
              user: { login: 'reviewer-a' },
            },
            {
              id: 702,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-702',
              body: 'Needs regression coverage before we merge this.',
              submitted_at: '2026-04-02T13:00:00.000Z',
              state: 'COMMENTED',
              user: { login: 'reviewer-b' },
            },
            {
              id: 703,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-703',
              body: 'Security issue: validate auth tokens here.',
              submitted_at: '2026-04-02T14:00:00.000Z',
              state: 'COMMENTED',
              user: { login: 'reviewer-c' },
            },
            {
              id: 704,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-704',
              body: 'Security issue: check authorization here too.',
              submitted_at: '2026-04-02T14:05:00.000Z',
              state: 'COMMENTED',
              user: { login: 'reviewer-c' },
            },
          ],
          '/repos/acme/widget/pulls/7/comments?per_page=100': [],
        }),
        writeLine: (line) => lines.push(line),
      });

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const rulesDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        pending_candidates: Array<{ category: string; reviewer_count: number }>;
      };
      assert.equal(rulesDoc.pending_candidates.some((rule) => rule.category === 'qa' && rule.reviewer_count === 2), true);
      assert.equal(rulesDoc.pending_candidates.some((rule) => rule.category === 'security'), false);
      const output = lines.join('\n');
      assert.match(output, /Effective reviewer policy: trusted reviewers=reviewer-a,reviewer-b; blocked reviewers=reviewer-c; min distinct reviewers=2/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('shows effective review-rules mode from global config when repo defaults have no override', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-defaults-show-effective-review-rules-'));
    const homeDir = join(wd, 'home');
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env: { ...process.env } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      await githubCommand(
        ['defaults', 'set', 'acme/widget', '--considerations', 'style,qa,security'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          now: () => now,
          writeLine: () => {},
        },
      );

      const lines: string[] = [];
      await githubCommand(
        ['defaults', 'show', 'acme/widget'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          writeLine: (line) => lines.push(line),
        },
      );

      const output = lines.join('\n');
      assert.match(output, /Repo review-rules mode for acme\/widget: \(none\)/i);
      assert.match(output, /Effective review-rules mode for acme\/widget: automatic/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('keeps repo-level manual review-rules mode even when the global default is automatic', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-mode-override-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-mode-override');
      const barePath = join(wd, 'mode-override-remote.git');
      await mkdir(join(seed, 'src', 'api'), { recursive: true });
      await writeFile(join(seed, 'src', 'api', 'client.ts'), 'export function searchDocuments(query: string): string { return query.trim(); }\n', 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      await githubCommand(['defaults', 'set', 'acme/widget', '--review-rules-mode', 'manual'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      await githubReviewRulesCommand(['scan', 'acme/widget'], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub({
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/pulls?state=all&per_page=100&page=1': [
            {
              number: 7,
              title: 'Tighten API handling',
              body: null,
              html_url: 'https://github.com/acme/widget/pull/7',
              state: 'closed',
              updated_at: '2026-04-02T11:00:00.000Z',
              head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
              base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
            },
          ],
          '/repos/acme/widget/pulls?state=all&per_page=100&page=2': [],
          '/repos/acme/widget/pulls/7': {
            number: 7,
            title: 'Tighten API handling',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'closed',
            updated_at: '2026-04-02T11:00:00.000Z',
            head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
          '/repos/acme/widget/pulls/7/reviews?per_page=100': [
            {
              id: 701,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-701',
              body: 'Please add regression tests for this behavior change before merge.',
              submitted_at: '2026-04-02T12:00:00.000Z',
              state: 'CHANGES_REQUESTED',
              user: { login: 'reviewer-a' },
            },
            {
              id: 702,
              html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-702',
              body: 'Needs regression coverage before we merge this.',
              submitted_at: '2026-04-02T13:00:00.000Z',
              state: 'COMMENTED',
              user: { login: 'reviewer-b' },
            },
          ],
          '/repos/acme/widget/pulls/7/comments?per_page=100': [],
        }),
        writeLine: () => {},
      });

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const rulesDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        approved_rules: Array<{ category: string }>;
        pending_candidates: Array<{ category: string }>;
      };
      assert.equal(rulesDoc.approved_rules.length, 0);
      assert.equal(rulesDoc.pending_candidates.some((rule) => rule.category === 'qa'), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('does not resurrect disabled review rules during automatic refresh and supports lifecycle commands', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-lifecycle-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-review-rules-lifecycle');
      const barePath = join(wd, 'review-rules-lifecycle-remote.git');
      await mkdir(join(seed, 'src', 'api'), { recursive: true });
      await writeFile(join(seed, 'src', 'api', 'client.ts'), 'export function searchDocuments(query: string): string { return query.trim(); }\n', 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      const routes = {
        '/repos/acme/widget': {
          name: 'widget',
          full_name: repoSlug,
          clone_url: barePath,
          default_branch: 'main',
          html_url: 'https://github.com/acme/widget',
        },
        '/repos/acme/widget/pulls?state=all&per_page=100&page=1': [
          {
            number: 7,
            title: 'Lifecycle PR',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'closed',
            updated_at: '2026-04-02T11:00:00.000Z',
            head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
        ],
        '/repos/acme/widget/pulls?state=all&per_page=100&page=2': [],
        '/repos/acme/widget/pulls/7': {
          number: 7,
          title: 'Lifecycle PR',
          body: null,
          html_url: 'https://github.com/acme/widget/pull/7',
          state: 'closed',
          updated_at: '2026-04-02T11:00:00.000Z',
          head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
          base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
        },
        '/repos/acme/widget/pulls/7/reviews?per_page=100': [
          {
            id: 701,
            html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-701',
            body: 'Please add regression tests for this behavior change before merge.',
            submitted_at: '2026-04-02T12:00:00.000Z',
            state: 'CHANGES_REQUESTED',
            user: { login: 'reviewer-a' },
          },
          {
            id: 702,
            html_url: 'https://github.com/acme/widget/pull/7#pullrequestreview-702',
            body: 'Needs regression coverage before we merge this.',
            submitted_at: '2026-04-02T13:00:00.000Z',
            state: 'COMMENTED',
            user: { login: 'reviewer-b' },
          },
        ],
        '/repos/acme/widget/pulls/7/comments?per_page=100': [],
      } satisfies Record<string, unknown>;

      await githubReviewRulesCommand(['scan', 'acme/widget'], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: () => {},
      });

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const scanned = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        pending_candidates: Array<{ id: string; category: string }>;
      };
      const qaRuleId = scanned.pending_candidates.find((rule) => rule.category === 'qa')?.id;
      assert.ok(qaRuleId);

      await githubReviewRulesCommand(['approve', 'acme/widget', qaRuleId!], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: () => {},
      });

      await githubReviewRulesCommand(['disable', 'acme/widget', qaRuleId!], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: () => {},
      });

      const explainLines: string[] = [];
      await githubReviewRulesCommand(['explain', 'acme/widget', qaRuleId!], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: (line) => explainLines.push(line),
      });
      assert.match(explainLines.join('\n'), /Rule .* \(disabled\)/i);

      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });

      await githubReviewRulesCommand(['scan', 'acme/widget'], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: () => {},
      });

      const afterDisable = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        approved_rules: Array<{ id: string }>;
        disabled_rules: Array<{ id: string }>;
      };
      assert.equal(afterDisable.approved_rules.some((rule) => rule.id === qaRuleId), false);
      assert.equal(afterDisable.disabled_rules.some((rule) => rule.id === qaRuleId), true);

      await githubReviewRulesCommand(['enable', 'acme/widget', qaRuleId!], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: () => {},
      });
      await githubReviewRulesCommand(['archive', 'acme/widget', qaRuleId!], {
        env,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub(routes),
        writeLine: () => {},
      });

      const finalDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        approved_rules: Array<{ id: string }>;
        disabled_rules: Array<{ id: string }>;
        archived_rules: Array<{ id: string }>;
      };
      assert.equal(finalDoc.approved_rules.some((rule) => rule.id === qaRuleId), false);
      assert.equal(finalDoc.disabled_rules.some((rule) => rule.id === qaRuleId), false);
      assert.equal(finalDoc.archived_rules.some((rule) => rule.id === qaRuleId), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('falls back to current-checkout code context provenance when PR-head content is unavailable', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-review-rules-fallback-provenance-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed-fallback-provenance');
      const barePath = join(wd, 'fallback-provenance-remote.git');
      await mkdir(join(seed, 'src', 'api'), { recursive: true });
      await writeFile(join(seed, 'src', 'api', 'client.ts'), [
        'export function searchDocuments(query: string): string {',
        '  return query.trim();',
        '}',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      await githubReviewRulesCommand(['scan', 'acme/widget'], {
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        now: () => now,
        fetchImpl: createFetchStub({
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/pulls?state=all&per_page=100&page=1': [
            {
              number: 7,
              title: 'Tighten API handling',
              body: null,
              html_url: 'https://github.com/acme/widget/pull/7',
              state: 'closed',
              updated_at: '2026-04-02T11:00:00.000Z',
              head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
              base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
            },
          ],
          '/repos/acme/widget/pulls?state=all&per_page=100&page=2': [],
          '/repos/acme/widget/pulls/7': {
            number: 7,
            title: 'Tighten API handling',
            body: null,
            html_url: 'https://github.com/acme/widget/pull/7',
            state: 'closed',
            updated_at: '2026-04-02T11:00:00.000Z',
            head: { ref: 'feature/pr-7', sha: 'sha-pr-7', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'sha-main', repo: { full_name: repoSlug } },
          },
          '/repos/acme/widget/pulls/7/reviews?per_page=100': [],
          '/repos/acme/widget/pulls/7/comments?per_page=100': [
            {
              id: 802,
              html_url: 'https://github.com/acme/widget/pull/7#discussion_r802',
              body: 'Avoid breaking the public API contract for callers.',
              created_at: '2026-04-02T13:05:00.000Z',
              updated_at: '2026-04-02T13:05:00.000Z',
              path: 'src/api/client.ts',
              line: 1,
              diff_hunk: '@@ -1,3 +1,3 @@',
              user: { login: 'reviewer-b' },
              pull_request_review_id: 702,
            },
            {
              id: 803,
              html_url: 'https://github.com/acme/widget/pull/7#discussion_r803',
              body: 'Do not break the public API contract here either.',
              created_at: '2026-04-02T13:06:00.000Z',
              updated_at: '2026-04-02T13:06:00.000Z',
              path: 'src/api/client.ts',
              line: 1,
              diff_hunk: '@@ -1,3 +1,3 @@',
              user: { login: 'reviewer-c' },
              pull_request_review_id: 702,
            },
          ],
        }),
        writeLine: () => {},
      });

      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const rulesDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as {
        pending_candidates: Array<{ category: string; evidence: Array<{ code_context_provenance?: string }> }>;
      };
      const apiRule = rulesDoc.pending_candidates.find((rule) => rule.category === 'api');
      assert.equal(apiRule?.evidence.every((evidence) => evidence.code_context_provenance === 'current_checkout'), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('starts from a GitHub issue URL, clones into managed storage, and launches inside a managed sandbox', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-start-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    try {
      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let launchedArgs: string[] | null = null;
      let launchCwd = '';
      let appendixPath = '';
      let leaseExistsDuringLaunch = false;
      let launchPath = '';
      let launchCodexHome = '';
      const issueBranch = `nana/issue-42/${issueSandboxId}`;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async (args) => {
            launchedArgs = args;
            launchCwd = process.cwd();
            if (!appendixPath) appendixPath = env[GITHUB_APPEND_ENV] || '';
            leaseExistsDuringLaunch = existsSync(join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandbox-locks', issueSandboxId, 'lease.json'));
            launchPath = env.PATH || '';
            launchCodexHome = env.CODEX_HOME || '';
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const sandboxPath = join(managedRepoRoot, 'sandboxes', issueSandboxId);
      const repoCheckoutPath = join(sandboxPath, 'repo');
      assert.deepEqual(launchedArgs, ['--dangerously-bypass-approvals-and-sandbox', 'Implement GitHub issue #42 for acme/widget']);
      assert.equal(launchCwd, sandboxPath);
      assert.equal(leaseExistsDuringLaunch, true);
      assert.equal(existsSync(appendixPath), true);
      assert.ok(appendixPath.endsWith('start-instructions.md'));
      assert.equal(existsSync(join(repoCheckoutPath, '.git')), true);
      assert.equal(existsSync(join(managedRepoRoot, 'source', '.git')), true);
      assert.equal(existsSync(join(repoCheckoutPath, '.nana')), false);
      assert.equal(existsSync(join(repoCheckoutPath, '.codex')), false);
      assert.equal(existsSync(join(sandboxPath, '.nana', 'sandbox.json')), true);
      assert.equal(launchCodexHome, join(sandboxPath, '.codex'));
      assert.equal(launchPath.startsWith(`${join(sandboxPath, '.nana', 'bin')}:`), true);
      assert.equal(existsSync(join(sandboxPath, '.codex', 'auth.json')), true);
      assert.equal(existsSync(join(sandboxPath, '.codex', 'config.toml')), true);
      assert.equal(existsSync(join(sandboxPath, '.codex', 'AGENTS.md')), true);
      assert.equal(existsSync(join(sandboxPath, '.nana', 'bin', 'nana')), true);
      const authStat = await lstat(join(sandboxPath, '.codex', 'auth.json'));
      assert.equal(authStat.isSymbolicLink(), true);
      const configStat = await lstat(join(sandboxPath, '.codex', 'config.toml'));
      assert.equal(configStat.isSymbolicLink(), false);
      const codexConfig = await readFile(join(sandboxPath, '.codex', 'config.toml'), 'utf-8');
      assert.match(codexConfig, /multi_agent = false/);
      assert.match(codexConfig, /child_agents_md = false/);
      assert.doesNotMatch(codexConfig, /\[mcp_servers\."docker-mcp"\]/);
      assert.match(codexConfig, /trust_level = "trusted"/);
      const appendix = await readFile(appendixPath, 'utf-8');
      assert.match(appendix, /Active considerations: none/i);
      assert.match(appendix, /Bootstrap loop:/i);
      assert.match(appendix, /Hardening loop:/i);
      assert.match(appendix, /Pipeline:/i);
      assert.match(appendix, /bootstrap:/i);
      assert.match(appendix, /coder -> executor \[execute, owner=self, blocking\]/i);
      assert.match(appendix, /impl:/i);
      assert.match(appendix, /Execution policy:/i);
      assert.match(appendix, /owner=self/i);
      assert.match(appendix, /owner=coder/i);
      assert.match(appendix, /Default completion mode is local-only/i);
      assert.match(appendix, /Do not push or open a PR automatically/i);

      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const globalLatest = JSON.parse(await readFile(join(homeDir, '.nana', 'github-workon', 'latest-run.json'), 'utf-8')) as { repo_root: string; run_id: string };
      assert.equal(globalLatest.repo_root, managedRepoRoot);
      assert.equal(globalLatest.run_id, latest.run_id);

      const manifest = JSON.parse(await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json'), 'utf-8')) as {
        repo_slug: string;
        sandbox_id: string;
        sandbox_path: string;
        sandbox_repo_path: string;
        managed_repo_root: string;
        verification_plan?: { source: string; source_files: Array<{ path: string; checksum: string; kind: string }>; lint: string[]; compile: string[]; unit: string[]; integration: string[] };
        verification_scripts_dir?: string;
        considerations_active: string[];
        role_layout: string;
        consideration_pipeline: Array<{ alias: string; role: string; phase: string; mode: string; owner: string; blocking: boolean }>;
        lane_prompt_artifacts: Array<{ alias: string; role: string; prompt_path: string; prompt_roles: string[] }>;
        team_resolved_aliases: string[];
        team_resolved_roles: string[];
      };
      assert.equal(manifest.repo_slug, repoSlug);
      assert.equal(manifest.sandbox_id, issueSandboxId);
      assert.equal(manifest.sandbox_path, sandboxPath);
      assert.equal(manifest.sandbox_repo_path, repoCheckoutPath);
      assert.equal(manifest.managed_repo_root, managedRepoRoot);
      assert.equal(manifest.verification_plan?.source, 'heuristic');
      assert.ok(Array.isArray(manifest.verification_plan?.source_files));
      assert.equal(manifest.verification_scripts_dir, join(sandboxPath, '.nana', 'work-on', 'verify'));
      assert.deepEqual(manifest.considerations_active, []);
      assert.equal(manifest.role_layout, 'split');
      assert.equal(manifest.consideration_pipeline.length, 1);
      assert.deepEqual(manifest.lane_prompt_artifacts, []);
      assert.deepEqual(
        (({ alias, role, phase, mode, owner, blocking }) => ({ alias, role, phase, mode, owner, blocking }))(manifest.consideration_pipeline[0]),
        {
          alias: 'coder',
          role: 'executor',
          phase: 'impl',
          mode: 'execute',
          owner: 'self',
          blocking: true,
        },
      );
      assert.deepEqual(manifest.team_resolved_aliases, ['coder']);
      assert.deepEqual(manifest.team_resolved_roles, ['executor']);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('infers and persists default considerations on the first managed run for a repo', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-infer-default-considerations-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget-client-sdk';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed');
      const barePath = join(wd, 'remote.git');
      await mkdir(join(seed, '.github', 'workflows'), { recursive: true });
      await mkdir(join(seed, 'docs'), { recursive: true });
      await mkdir(join(seed, 'docs', 'openapi'), { recursive: true });
      await writeFile(join(seed, 'README.md'), [
        '# Widget Client SDK',
        '',
        'Public API client library.',
        'Includes auth token handling and TLS support.',
        'Contains benchmark notes for throughput.',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, 'docs', 'architecture.md'), '# Architecture\n\nProtocol and design notes.\n', 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'search.yaml'), [
        'paths:',
        '  /search:',
        '    get:',
        '      summary: Search endpoint',
        '      description: Hot path search API with p99 latency budget.',
        '      operationId: searchDocuments',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, 'pom.xml'), '<project><artifactId>widget-client-sdk</artifactId></project>\n', 'utf-8');
      await writeFile(join(seed, 'Makefile'), [
        'lint:',
        '\techo lint',
        'compile:',
        '\techo compile',
        'test-unit:',
        '\techo test-unit',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, '.github', 'workflows', 'continuous-integration.yml'), [
        'jobs:',
        '  verify:',
        '    steps:',
        '      - run: make lint',
        '      - run: make compile',
        '      - run: make test-unit',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      const output: string[] = [];

      await githubCommand(
        ['start', 'https://github.com/acme/widget-client-sdk/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget-client-sdk': {
              name: 'widget-client-sdk',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget-client-sdk',
            },
            '/repos/acme/widget-client-sdk/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget-client-sdk/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            '/repos/acme/widget-client-sdk/issues/42/comments?per_page=100': [],
            '/repos/acme/widget-client-sdk/pulls?state=all&head=acme%3Anana%2Fissue-42%2Fissue-42-pr-177521040000&per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: (line) => output.push(line),
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget-client-sdk');
      const settings = JSON.parse(await readFile(join(managedRepoRoot, 'settings.json'), 'utf-8')) as {
        default_considerations: string[];
        hot_path_api_profile?: { hot_path_api_files?: string[] };
      };
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const manifest = JSON.parse(await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json'), 'utf-8')) as {
        considerations_active: string[];
      };

      assert.deepEqual(settings.default_considerations, ['arch', 'dependency', 'api', 'perf', 'style', 'security', 'qa']);
      assert.deepEqual(settings.hot_path_api_profile?.hot_path_api_files, ['docs/openapi/search.yaml']);
      assert.deepEqual(manifest.considerations_active, ['arch', 'dependency', 'api', 'perf', 'style', 'security', 'qa']);
      assert.ok(output.some((line) => /First managed run for acme\/widget-client-sdk; inferred default considerations: arch, dependency, api, perf, style, security, qa\./i.test(line)));
      assert.ok(output.some((line) => /arch: architecture or design signals detected/i.test(line)));
      assert.ok(output.some((line) => /dependency: dependency manifests detected/i.test(line)));
      assert.ok(output.some((line) => /style: lint or style verification commands detected/i.test(line)));
      assert.ok(output.some((line) => /api: library or API-facing repository signals detected/i.test(line)));
      assert.ok(output.some((line) => /security: security-sensitive or network-facing repository signals detected/i.test(line)));
      assert.ok(output.some((line) => /perf: performance or benchmark signals detected/i.test(line)));
      assert.ok(output.some((line) => /qa: test directories or unit verification commands detected/i.test(line)));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('applies repo hot-path API override files when persisting managed repo settings', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-hotpath-override-settings-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget-client-sdk';
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const seed = join(wd, 'seed');
      const barePath = join(wd, 'remote.git');
      await mkdir(join(seed, '.github', 'workflows'), { recursive: true });
      await mkdir(join(seed, '.nana'), { recursive: true });
      await mkdir(join(seed, 'docs', 'openapi'), { recursive: true });
      await writeFile(join(seed, 'README.md'), 'Widget client library.\n', 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'search.yaml'), [
        'paths:',
        '  /search:',
        '    get:',
        '      description: Search endpoint.',
        '      operationId: searchDocuments',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'admin.yaml'), [
        'paths:',
        '  /admin/reindex:',
        '    post:',
        '      description: Administrative endpoint.',
        '      operationId: adminReindex',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, '.nana', 'work-on-hot-path-apis.json'), JSON.stringify({
        version: 1,
        hot_path_api_files: ['docs/openapi/admin.yaml'],
        api_identifier_tokens: ['adminReindex'],
      }, null, 2), 'utf-8');
      await writeFile(join(seed, 'pom.xml'), '<project><artifactId>widget-client-sdk</artifactId></project>\n', 'utf-8');
      await writeFile(join(seed, '.github', 'workflows', 'continuous-integration.yml'), [
        'jobs:',
        '  verify:',
        '    steps:',
        '      - run: echo lint',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      await githubCommand(
        ['start', 'https://github.com/acme/widget-client-sdk/issues/42'],
        {
          env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget-client-sdk': {
              name: 'widget-client-sdk',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget-client-sdk',
            },
            '/repos/acme/widget-client-sdk/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget-client-sdk/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            '/repos/acme/widget-client-sdk/issues/42/comments?per_page=100': [],
            '/repos/acme/widget-client-sdk/pulls?state=all&head=acme%3Anana%2Fissue-42%2Fissue-42-pr-177521040000&per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget-client-sdk');
      const settings = JSON.parse(await readFile(join(managedRepoRoot, 'settings.json'), 'utf-8')) as {
        hot_path_api_profile?: { hot_path_api_files?: string[]; api_identifier_tokens?: string[] };
      };
      assert.deepEqual(settings.hot_path_api_profile?.hot_path_api_files, ['docs/openapi/admin.yaml']);
      assert.equal(settings.hot_path_api_profile?.api_identifier_tokens?.includes('adminReindex'), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('refreshes sandbox verification artifacts when tracked source files drift', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-verify-refresh-'));
    const homeDir = join(wd, 'home');
    const codexHomeDir = defaultUserCodexHome(homeDir);
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const seed = join(wd, 'seed');
      const barePath = join(wd, 'remote.git');
      await mkdir(join(seed, '.github', 'workflows'), { recursive: true });
      await writeFile(join(seed, 'Makefile'), [
        'lint:',
        '\techo lint-v1',
        'compile:',
        '\techo compile-v1',
        'test-unit:',
        '\techo test-v1',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, '.github', 'workflows', 'continuous-integration.yml'), [
        'jobs:',
        '  verify:',
        '    steps:',
        '      - run: make lint',
        '      - run: make compile',
        '      - run: make test-unit',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      await mkdir(join(codexHomeDir, 'agents'), { recursive: true });
      await writeFile(join(codexHomeDir, 'auth.json'), '{"token":"test"}\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'config.toml'), 'model = "gpt-5.4"\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'AGENTS.md'), '# sandbox bootstrap\n', 'utf-8');
      await writeFile(join(codexHomeDir, 'agents', 'executor.toml'), 'name = "executor"\n', 'utf-8');

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      const output: string[] = [];

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
          }),
          launchWithHud: async () => {},
          writeLine: (line) => output.push(line),
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const manifestPath = join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json');
      const repoPlanPath = join(managedRepoRoot, 'verification-plan.json');
      const beforeManifest = JSON.parse(await readFile(manifestPath, 'utf-8')) as {
        sandbox_path: string;
        sandbox_repo_path: string;
        sandbox_id: string;
        verification_plan: { plan_fingerprint: string; source_files: Array<{ path: string; checksum: string }> };
      };
      const sandboxVerifyDir = join(beforeManifest.sandbox_path, '.nana', 'work-on', 'verify');
      const repoUnitHistoryPath = join(managedRepoRoot, 'verification-unit-history.tsv');
      const sandboxUnitHistoryPath = join(sandboxVerifyDir, 'unit-history.tsv');
      const sandboxPolicyPath = join(sandboxVerifyDir, 'unit-policy.env');
      const repoDriftLogPath = join(managedRepoRoot, 'verification-drift-events.jsonl');
      const sandboxDriftLogPath = join(sandboxVerifyDir, 'drift-events.jsonl');
      const beforeChecksum = beforeManifest.verification_plan.source_files.find((file) => file.path === 'Makefile')?.checksum;
      assert.ok(beforeChecksum);
      await writeFile(
        repoUnitHistoryPath,
        `2026-04-03T10:00:00Z\t${beforeManifest.sandbox_id}\t1000\tpass\t${beforeManifest.verification_plan.plan_fingerprint}\n`,
        'utf-8',
      );
      await writeFile(
        sandboxUnitHistoryPath,
        `2026-04-03T10:00:00Z\t${beforeManifest.sandbox_id}\t900\tpass\t${beforeManifest.verification_plan.plan_fingerprint}\n`,
        'utf-8',
      );

      await writeFile(join(beforeManifest.sandbox_repo_path, 'Makefile'), [
        'lint:',
        '\techo lint-v2',
        'compile:',
        '\techo compile-v2',
        'test-unit:',
        '\techo test-v2',
        '',
      ].join('\n'), 'utf-8');

      await githubCommand(
        ['verify-refresh', '--last'],
        {
          env,
          homeDir,
          writeLine: (line) => output.push(line),
        },
      );

      const afterManifest = JSON.parse(await readFile(manifestPath, 'utf-8')) as {
        verification_plan: {
          plan_fingerprint: string;
          lint: string[];
          compile: string[];
          unit: string[];
          integration: string[];
          source_files: Array<{ path: string; checksum: string }>;
        };
      };
      const repoPlan = JSON.parse(await readFile(repoPlanPath, 'utf-8')) as {
        source_files: Array<{ path: string; checksum: string }>;
      };
      const afterChecksum = afterManifest.verification_plan.source_files.find((file) => file.path === 'Makefile')?.checksum;
      const planJson = JSON.parse(await readFile(join(sandboxVerifyDir, 'plan.json'), 'utf-8')) as {
        plan_fingerprint: string;
        unit_policy: { mode: string; sample_count: number; plan_fingerprint: string };
        integration_policy: { mode: string };
        source_files: Array<{ path: string; checksum: string }>;
      };
      const policyEnv = await readFile(sandboxPolicyPath, 'utf-8');
      const refreshScript = await readFile(join(sandboxVerifyDir, 'refresh.sh'), 'utf-8');
      const repoDriftLog = (await readFile(repoDriftLogPath, 'utf-8')).trim().split('\n').filter(Boolean).map((line) => JSON.parse(line) as { reason: string; changed_sources: Array<{ path: string; change: string }> });
      const sandboxDriftLog = (await readFile(sandboxDriftLogPath, 'utf-8')).trim().split('\n').filter(Boolean).map((line) => JSON.parse(line) as { reason: string; changed_sources: Array<{ path: string; change: string }> });

      assert.notEqual(afterChecksum, beforeChecksum);
      assert.notEqual(afterManifest.verification_plan.plan_fingerprint, beforeManifest.verification_plan.plan_fingerprint);
      assert.deepEqual(afterManifest.verification_plan.lint, ['make lint']);
      assert.deepEqual(afterManifest.verification_plan.compile, ['make compile']);
      assert.deepEqual(afterManifest.verification_plan.unit, ['make test-unit']);
      assert.deepEqual(afterManifest.verification_plan.integration, []);
      assert.equal(planJson.source_files.find((file) => file.path === 'Makefile')?.checksum, afterChecksum);
      assert.equal(planJson.plan_fingerprint, afterManifest.verification_plan.plan_fingerprint);
      assert.equal(planJson.unit_policy.mode, 'unknown');
      assert.equal(planJson.unit_policy.sample_count, 0);
      assert.equal(planJson.unit_policy.plan_fingerprint, afterManifest.verification_plan.plan_fingerprint);
      assert.equal(planJson.integration_policy.mode, 'none');
      assert.match(policyEnv, /NANA_WORKON_UNIT_TESTS_MODE=unknown/);
      assert.match(policyEnv, new RegExp(afterManifest.verification_plan.plan_fingerprint.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
      assert.equal(repoPlan.source_files.find((file) => file.path === 'Makefile')?.checksum, beforeChecksum);
      assert.ok(repoDriftLog.some((event) => event.reason === 'plan-drift' && event.changed_sources.some((source) => source.path === 'Makefile' && source.change === 'modified')));
      assert.ok(sandboxDriftLog.some((event) => event.reason === 'plan-drift' && event.changed_sources.some((source) => source.path === 'Makefile' && source.change === 'modified')));
      assert.match(refreshScript, /(?:nana work-on|nana\.js' work-on) verify-refresh --run-id /);
      assert.match(refreshScript, new RegExp(latest.run_id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
      assert.ok(output.some((line) => /Verification artifacts for run .* refreshed\./.test(line)));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('writes mapped consideration-composed roster instructions', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-team-mode-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
        NANA_GITHUB_CI_POLL_INTERVAL_MS: '1',
        NANA_GITHUB_CI_TIMEOUT_MS: '2000',
      } as NodeJS.ProcessEnv;
      let appendixPath = '';
      let repoCheckoutPath = '';

      await githubCommand(
        [
          'start',
          'https://github.com/acme/widget/issues/42',
          '--considerations',
          'arch,perf,api',
        ],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: async (input, init) => {
            const url = typeof input === 'string'
              ? new URL(input)
              : input instanceof URL
                ? input
                : new URL(input.url);
            const key = `${url.pathname}${url.search}`;
            const method = (init?.method || 'GET').toUpperCase();

            if (method === 'GET' && key === '/user') return jsonResponse({ login: 'dkropachev' });
            if (method === 'GET' && key === '/repos/acme/widget') {
              return jsonResponse({
                name: 'widget',
                full_name: repoSlug,
                clone_url: barePath,
                default_branch: 'main',
                html_url: 'https://github.com/acme/widget',
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
              return jsonResponse({
                number: 42,
                title: 'Implement queue healing',
                body: 'Need to keep workers alive after review updates.',
                html_url: 'https://github.com/acme/widget/issues/42',
                state: 'open',
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
              });
            }
            if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
              return jsonResponse([]);
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
              return jsonResponse([]);
            }
            if (method === 'POST' && key === '/repos/acme/widget/pulls') {
              return jsonResponse({
                number: 91,
                title: 'Implement queue healing',
                body: 'body',
                html_url: 'https://github.com/acme/widget/pull/91',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T10:02:00.000Z',
                user: { login: 'dkropachev' },
                head: { ref: issueBranch, sha: 'placeholder', repo: { full_name: repoSlug } },
                base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/commits/') && key.endsWith('/check-runs?per_page=100')) {
              return jsonResponse({
                total_count: 1,
                check_runs: [{ id: 1, name: 'Build', status: 'completed', conclusion: 'success' }],
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
              const sha = execFileSync('git', ['rev-parse', 'HEAD'], {
                cwd: repoCheckoutPath,
                encoding: 'utf-8',
                stdio: ['ignore', 'pipe', 'pipe'],
              }).trim();
              return jsonResponse({
                total_count: 1,
                workflow_runs: [{ id: 77, name: 'CI', head_sha: sha, status: 'completed', conclusion: 'success', html_url: 'https://example.invalid/actions/77' }],
              });
            }
            return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
          },
          launchWithHud: async () => {
            if (!appendixPath) appendixPath = env[GITHUB_APPEND_ENV] || '';
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
          },
          writeLine: () => {},
        },
      );

      const appendix = await readFile(appendixPath, 'utf-8');
      assert.doesNotMatch(appendix, /Execution mode:/i);
      assert.match(appendix, /Active considerations: arch, perf, api/i);
      assert.match(appendix, /isolated Codex lane processes/i);
      assert.match(appendix, /nana work-on lane-exec --run-id/i);
      assert.match(appendix, /Do not run every lane immediately/i);
      assert.match(appendix, /bootstrap reviewer lanes still use isolated lane processes/i);
      assert.match(appendix, /bootstrap:/i);
      assert.match(appendix, /hardening:/i);
      assert.match(appendix, /pre-impl:/i);
      assert.match(appendix, /architect -> architect\+executor \[review\+execute, owner=self, blocking\]/i);
      assert.match(appendix, /impl:/i);
      assert.match(appendix, /coder -> executor \[execute, owner=self, blocking\]/i);
      assert.match(appendix, /perf-coder -> executor \[execute, owner=self, advisory\]/i);
      assert.match(appendix, /post-impl:/i);
      assert.match(appendix, /perf-reviewer -> performance-reviewer \[review, owner=coder, blocking\]/i);
      assert.match(appendix, /api-reviewer -> api-reviewer \[review, owner=coder, blocking\]/i);
      assert.match(appendix, /lane command: nana work-on lane-exec --run-id /i);
      assert.match(appendix, /Blocking lanes must be satisfied before completion/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('writes merged reviewer+executor prompt artifacts when requested', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-merged-role-layout-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let appendixPath = '';

      await githubCommand(
        [
          'start',
          'https://github.com/acme/widget/issues/42',
          '--considerations',
          'security,style',
          '--role-layout',
          'reviewer+executor',
        ],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            if (!appendixPath) appendixPath = env[GITHUB_APPEND_ENV] || '';
          },
          writeLine: () => {},
        },
      );

      const appendix = await readFile(appendixPath, 'utf-8');
      assert.match(appendix, /Role layout: reviewer\+executor/i);
      assert.match(appendix, /Do not activate hardening lanes during bootstrap/i);
      assert.match(appendix, /bootstrap:/i);
      assert.match(appendix, /hardening:/i);
      assert.match(appendix, /security-reviewer -> security-reviewer\+executor \[review\+execute, owner=self, blocking\]/i);
      assert.match(appendix, /style-reviewer -> style-reviewer\+executor \[review\+execute, owner=self, advisory\]/i);
      assert.match(appendix, /merged prompt:/i);
      assert.match(appendix, /run one isolated lane process per lane alias/i);

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const manifest = JSON.parse(await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json'), 'utf-8')) as {
        role_layout: string;
        consideration_pipeline: Array<{ alias: string; role: string; mode: string; owner: string; prompt_roles?: string[] }>;
        lane_prompt_artifacts: Array<{ alias: string; role: string; prompt_path: string; prompt_roles: string[] }>;
      };

      assert.equal(manifest.role_layout, 'reviewer+executor');
      assert.ok(manifest.consideration_pipeline.some((lane) => lane.role === 'security-reviewer+executor' && lane.mode === 'review+execute' && lane.owner === 'self'));
      assert.ok(manifest.lane_prompt_artifacts.some((artifact) => artifact.role === 'security-reviewer+executor' && artifact.prompt_roles.join('+') === 'security-reviewer+executor'));

      const securityPrompt = manifest.lane_prompt_artifacts.find((artifact) => artifact.role === 'security-reviewer+executor');
      assert.ok(securityPrompt);
      assert.equal(existsSync(securityPrompt!.prompt_path), true);
      const mergedPrompt = await readFile(securityPrompt!.prompt_path, 'utf-8');
      assert.match(mergedPrompt, /# NANA merged lane prompt: security-reviewer/i);
      assert.match(mergedPrompt, /Source prompts: security-reviewer \+ executor/i);
      assert.match(mergedPrompt, /## Reviewer Specialization \(security-reviewer\)/i);
      assert.match(mergedPrompt, /## Executor Delivery Contract \(executor\)/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('automatically publishes a draft PR and records the published PR in the manifest', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-autopublish-pr-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
        NANA_GITHUB_CI_POLL_INTERVAL_MS: '1',
        NANA_GITHUB_CI_TIMEOUT_MS: '2000',
      } as NodeJS.ProcessEnv;
      let repoCheckoutPath = '';
      let createdPrBody = '';
      let createdPrHead = '';
      let createdPrTitle = '';

      const fetchImpl: typeof fetch = async (input, init) => {
        const url = typeof input === 'string'
          ? new URL(input)
          : input instanceof URL
            ? input
            : new URL(input.url);
        const key = `${url.pathname}${url.search}`;
        const method = (init?.method || 'GET').toUpperCase();

        if (method === 'GET' && key === '/user') {
          return jsonResponse({ login: 'dkropachev' });
        }
        if (method === 'GET' && key === '/repos/acme/widget') {
          return jsonResponse({
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          });
        }
        if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
          return jsonResponse({
            number: 42,
            title: 'Implement queue healing',
            body: 'Need to keep workers alive after review updates.',
            html_url: 'https://github.com/acme/widget/issues/42',
            state: 'open',
            updated_at: '2026-04-03T09:00:00.000Z',
            user: { login: 'requester' },
          });
        }
        if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
          return jsonResponse([]);
        }
        if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
          return jsonResponse([]);
        }
        if (method === 'POST' && key === '/repos/acme/widget/pulls') {
          const body = JSON.parse(String(init?.body || '{}')) as { title: string; head: string; body: string };
          createdPrTitle = body.title;
          createdPrHead = body.head;
          createdPrBody = body.body;
          return jsonResponse({
            number: 91,
            title: body.title,
            body: body.body,
            html_url: 'https://github.com/acme/widget/pull/91',
            state: 'open',
            merged_at: null,
            updated_at: '2026-04-03T10:02:00.000Z',
            user: { login: 'dkropachev' },
            head: { ref: issueBranch, sha: 'placeholder', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
            draft: true,
          });
        }
        if (method === 'GET' && key.startsWith('/repos/acme/widget/commits/') && key.endsWith('/check-runs?per_page=100')) {
          const sha = execFileSync('git', ['rev-parse', 'HEAD'], {
            cwd: repoCheckoutPath,
            encoding: 'utf-8',
            stdio: ['ignore', 'pipe', 'pipe'],
          }).trim();
          assert.match(key, new RegExp(sha));
          return jsonResponse({
            total_count: 1,
            check_runs: [
              {
                id: 1,
                name: 'Build',
                status: 'completed',
                conclusion: 'success',
                details_url: 'https://example.invalid/check/1',
              },
            ],
          });
        }
        if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
          const sha = execFileSync('git', ['rev-parse', 'HEAD'], {
            cwd: repoCheckoutPath,
            encoding: 'utf-8',
            stdio: ['ignore', 'pipe', 'pipe'],
          }).trim();
          assert.match(key, new RegExp(sha));
          return jsonResponse({
            total_count: 1,
            workflow_runs: [
              {
                id: 77,
                name: 'CI',
                head_sha: sha,
                status: 'completed',
                conclusion: 'success',
                html_url: 'https://example.invalid/actions/runs/77',
                event: 'pull_request',
              },
            ],
          });
        }

        return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
      };

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--create-pr'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl,
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch\n', 'utf-8');
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const manifest = JSON.parse(await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json'), 'utf-8')) as {
        published_pr_number?: number;
        published_pr_url?: string;
        published_pr_head_ref?: string;
        publication_state?: string;
      };

      assert.equal(createdPrTitle, 'Implement queue healing');
      assert.equal(createdPrHead, `acme:${issueBranch}`);
      assert.match(createdPrBody, /Closes https:\/\/github.com\/acme\/widget\/issues\/42/i);
      assert.equal(manifest.published_pr_number, 91);
      assert.equal(manifest.published_pr_url, 'https://github.com/acme/widget/pull/91');
      assert.equal(manifest.published_pr_head_ref, issueBranch);
      assert.equal(manifest.publication_state, 'ci_green');
      assert.equal(existsSync(join(managedRepoRoot, 'sandboxes', 'pr-91')), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('starts a background scheduler daemon when CI blocks in a recoverable state', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-publication-daemon-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
        NANA_GITHUB_CI_POLL_INTERVAL_MS: '1',
        NANA_GITHUB_CI_TIMEOUT_MS: '1',
      } as NodeJS.ProcessEnv;
      let daemonRunId = '';
      let repoCheckoutPath = '';

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--create-pr'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            daemonRunId = runId;
          },
          fetchImpl: async (input, init) => {
            const url = typeof input === 'string'
              ? new URL(input)
              : input instanceof URL
                ? input
                : new URL(input.url);
            const key = `${url.pathname}${url.search}`;
            const method = (init?.method || 'GET').toUpperCase();
            if (method === 'GET' && key === '/user') return jsonResponse({ login: 'dkropachev' });
            if (method === 'GET' && key === '/repos/acme/widget') {
              return jsonResponse({
                name: 'widget',
                full_name: repoSlug,
                clone_url: barePath,
                default_branch: 'main',
                html_url: 'https://github.com/acme/widget',
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
              return jsonResponse({
                number: 42,
                title: 'Implement queue healing',
                body: 'Need to keep workers alive after review updates.',
                html_url: 'https://github.com/acme/widget/issues/42',
                state: 'open',
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
              });
            }
            if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
              return jsonResponse([]);
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
              return jsonResponse([]);
            }
            if (method === 'POST' && key === '/repos/acme/widget/pulls') {
              return jsonResponse({
                number: 91,
                title: 'Implement queue healing',
                body: 'body',
                html_url: 'https://github.com/acme/widget/pull/91',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T10:02:00.000Z',
                user: { login: 'dkropachev' },
                head: { ref: issueBranch, sha: 'placeholder', repo: { full_name: repoSlug } },
                base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/commits/') && key.endsWith('/check-runs?per_page=100')) {
              return jsonResponse({
                total_count: 0,
                check_runs: [],
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
              const sha = execFileSync('git', ['rev-parse', 'HEAD'], {
                cwd: repoCheckoutPath,
                encoding: 'utf-8',
                stdio: ['ignore', 'pipe', 'pipe'],
              }).trim();
              return jsonResponse({
                total_count: 1,
                workflow_runs: [{ id: 77, name: 'CI', head_sha: sha, status: 'in_progress', conclusion: null, html_url: 'https://example.invalid/actions/77' }],
              });
            }
            return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
          },
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch\n', 'utf-8');
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const manifest = JSON.parse(await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json'), 'utf-8')) as {
        publication_state?: string;
      };

      assert.equal(daemonRunId, latest.run_id);
      assert.equal(manifest.publication_state, 'blocked');
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('starts a background scheduler daemon when hardening lanes are invalidated by later leader changes', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-scheduler-daemon-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
      } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            const fileName = launchCount === 1 ? 'feature.txt' : 'config/security.yml';
            await mkdir(dirname(join(repoCheckoutPath, fileName)), { recursive: true });
            await writeFile(join(repoCheckoutPath, fileName), `change ${launchCount}\n`, 'utf-8');
            execFileSync('git', ['add', fileName], { cwd: repoCheckoutPath, stdio: 'ignore' });
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'security-reviewer.json'), 'utf-8'),
      ) as { status: string; retry_count?: number };

      assert.equal(schedulerRunId, latest.run_id);
      assert.equal(laneState.status, 'pending');
      assert.equal(launchCount, 2);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('does not invalidate unrelated hardening lanes when later commits touch files outside their concern', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-scheduler-no-invalid-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
      } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            const fileName = launchCount === 1 ? 'feature.txt' : 'README.md';
            const fileContent = launchCount === 1 ? 'feature branch one\n' : '# widget updated\n';
            await writeFile(join(repoCheckoutPath, fileName), fileContent, 'utf-8');
            execFileSync('git', ['add', fileName], { cwd: repoCheckoutPath, stdio: 'ignore' });
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'security-reviewer.json'), 'utf-8'),
      ) as { status: string; invalidated_reason?: string };

      assert.equal(launchCount, 2);
      assert.equal(schedulerRunId, '');
      assert.equal(laneState.status, 'completed');
      assert.equal(laneState.invalidated_reason, undefined);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('requeues retryable hardening lane failures and starts the scheduler daemon', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-lane-retry-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
      } as NodeJS.ProcessEnv;
      let schedulerRunId = '';

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            if (lane.alias === 'security-reviewer') {
              await writeFile(resultPath, 'spawn ENOENT codex\n', 'utf-8');
              return {
                output: 'spawn ENOENT codex\n',
                resultPath,
                laneCodexHome: 'test://lane',
                status: 'failed' as const,
              };
            }
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'security-reviewer.json'), 'utf-8'),
      ) as { status: string; retry_count?: number; failure_category?: string };

      assert.equal(schedulerRunId, latest.run_id);
      assert.equal(laneState.status, 'pending');
      assert.equal(laneState.retry_count, 1);
      assert.equal(laneState.failure_category, 'launch_failure');
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('applies repo concern override files when invalidating hardening lanes', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-concern-override-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            if (launchCount === 1) {
              await mkdir(join(repoCheckoutPath, '.nana'), { recursive: true });
              await writeFile(
                join(repoCheckoutPath, '.nana', 'work-on-concerns.json'),
                JSON.stringify({
                  version: 1,
                  lanes: {
                    'security-reviewer': {
                      pathPrefixes: ['policies/'],
                    },
                  },
                }, null, 2),
                'utf-8',
              );
              await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch one\n', 'utf-8');
              execFileSync('git', ['add', '.nana/work-on-concerns.json', 'feature.txt'], { cwd: repoCheckoutPath, stdio: 'ignore' });
            } else {
              await mkdir(join(repoCheckoutPath, 'policies'), { recursive: true });
              await writeFile(join(repoCheckoutPath, 'policies', 'access.matrix'), 'admins: all\n', 'utf-8');
              execFileSync('git', ['add', 'policies/access.matrix'], { cwd: repoCheckoutPath, stdio: 'ignore' });
            }
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'security-reviewer.json'), 'utf-8'),
      ) as { status: string; invalidation_concern_match?: { matched_files?: string[] } };

      assert.equal(launchCount, 2);
      assert.equal(schedulerRunId, latest.run_id);
      assert.equal(laneState.status, 'pending');
      assert.deepEqual(laneState.invalidation_concern_match?.matched_files, ['policies/access.matrix']);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('keeps unknown-file invalidation conservative for hardening lanes', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-unknown-file-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            const fileName = launchCount === 1 ? 'feature.txt' : 'misc/notes.txt';
            await mkdir(dirname(join(repoCheckoutPath, fileName)), { recursive: true });
            await writeFile(join(repoCheckoutPath, fileName), `change ${launchCount}\n`, 'utf-8');
            execFileSync('git', ['add', fileName], { cwd: repoCheckoutPath, stdio: 'ignore' });
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'security-reviewer.json'), 'utf-8'),
      ) as { status: string; invalidated_reason?: string };

      assert.equal(launchCount, 2);
      assert.equal(schedulerRunId, '');
      assert.equal(laneState.status, 'completed');
      assert.equal(laneState.invalidated_reason, undefined);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('invalidates perf lanes when later commits touch a persisted hot-path API surface', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-perf-hotpath-invalid-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const seed = join(wd, 'seed');
      const barePath = join(wd, 'remote.git');
      await mkdir(join(seed, '.github', 'workflows'), { recursive: true });
      await mkdir(join(seed, 'docs', 'openapi'), { recursive: true });
      await writeFile(join(seed, 'README.md'), 'Widget service with benchmark-sensitive search API.\n', 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'search.yaml'), [
        'paths:',
        '  /search:',
        '    get:',
        '      description: Hot path search API with p99 latency budget.',
        '      operationId: searchDocuments',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'perf'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            const fileName = launchCount === 1 ? 'feature.txt' : 'docs/openapi/search.yaml';
            const content = launchCount === 1
              ? 'feature branch one\n'
              : [
                'paths:',
                '  /search:',
                '    get:',
                '      description: Hot path search API with stricter p99 budget.',
                '      operationId: searchDocuments',
                '',
              ].join('\n');
            await mkdir(dirname(join(repoCheckoutPath, fileName)), { recursive: true });
            await writeFile(join(repoCheckoutPath, fileName), content, 'utf-8');
            execFileSync('git', ['add', fileName], { cwd: repoCheckoutPath, stdio: 'ignore' });
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'perf-reviewer.json'), 'utf-8'),
      ) as { status: string; invalidation_concern_match?: { matched_files?: string[] } };

      assert.equal(launchCount, 2);
      assert.equal(schedulerRunId, latest.run_id);
      assert.equal(laneState.status, 'pending');
      assert.deepEqual(laneState.invalidation_concern_match?.matched_files, ['docs/openapi/search.yaml']);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('does not invalidate perf lanes for non-hot-path API changes', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-perf-nonhot-invalid-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const seed = join(wd, 'seed');
      const barePath = join(wd, 'remote.git');
      await mkdir(join(seed, 'docs', 'openapi'), { recursive: true });
      await writeFile(join(seed, 'README.md'), 'Widget service with benchmark-sensitive search API.\n', 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'search.yaml'), [
        'paths:',
        '  /search:',
        '    get:',
        '      description: Hot path search API with p99 latency budget.',
        '      operationId: searchDocuments',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'admin.yaml'), [
        'paths:',
        '  /admin/reindex:',
        '    post:',
        '      description: Administrative endpoint.',
        '      operationId: adminReindex',
        '',
      ].join('\n'), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'perf'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            const fileName = launchCount === 1 ? 'feature.txt' : 'docs/openapi/admin.yaml';
            const content = launchCount === 1
              ? 'feature branch one\n'
              : [
                'paths:',
                '  /admin/reindex:',
                '    post:',
                '      description: Administrative endpoint for maintenance tasks.',
                '      operationId: adminReindex',
                '',
              ].join('\n');
            await mkdir(dirname(join(repoCheckoutPath, fileName)), { recursive: true });
            await writeFile(join(repoCheckoutPath, fileName), content, 'utf-8');
            execFileSync('git', ['add', fileName], { cwd: repoCheckoutPath, stdio: 'ignore' });
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'perf-reviewer.json'), 'utf-8'),
      ) as { status: string; invalidated_reason?: string };

      assert.equal(launchCount, 2);
      assert.equal(schedulerRunId, '');
      assert.equal(laneState.status, 'completed');
      assert.equal(laneState.invalidated_reason, undefined);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('invalidates perf lanes for override-defined hot-path API changes even without perf keywords', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-perf-hotpath-override-invalid-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const seed = join(wd, 'seed');
      const barePath = join(wd, 'remote.git');
      await mkdir(join(seed, '.nana'), { recursive: true });
      await mkdir(join(seed, 'docs', 'openapi'), { recursive: true });
      await writeFile(join(seed, 'README.md'), 'Widget service.\n', 'utf-8');
      await writeFile(join(seed, 'docs', 'openapi', 'admin.yaml'), [
        'paths:',
        '  /admin/reindex:',
        '    post:',
        '      description: Administrative endpoint.',
        '      operationId: adminReindex',
        '',
      ].join('\n'), 'utf-8');
      await writeFile(join(seed, '.nana', 'work-on-hot-path-apis.json'), JSON.stringify({
        version: 1,
        hot_path_api_files: ['docs/openapi/admin.yaml'],
        api_identifier_tokens: ['adminReindex'],
      }, null, 2), 'utf-8');
      initGitRepo(seed);
      execFileSync('git', ['clone', '--bare', seed, barePath], { stdio: 'ignore' });

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let schedulerRunId = '';
      let repoCheckoutPath = '';
      let launchCount = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'perf'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            schedulerRunId = runId;
          },
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            launchCount += 1;
            const fileName = launchCount === 1 ? 'feature.txt' : 'docs/openapi/admin.yaml';
            const content = launchCount === 1
              ? 'feature branch one\n'
              : [
                'paths:',
                '  /admin/reindex:',
                '    post:',
                '      description: Administrative endpoint for reindex jobs.',
                '      operationId: adminReindex',
                '',
              ].join('\n');
            await mkdir(dirname(join(repoCheckoutPath, fileName)), { recursive: true });
            await writeFile(join(repoCheckoutPath, fileName), content, 'utf-8');
            execFileSync('git', ['add', fileName], { cwd: repoCheckoutPath, stdio: 'ignore' });
            execFileSync('git', ['commit', '-m', `change-${launchCount}`], { cwd: repoCheckoutPath, stdio: 'ignore' });
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const laneState = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'perf-reviewer.json'), 'utf-8'),
      ) as { status: string; invalidation_concern_match?: { matched_files?: string[] } };

      assert.equal(launchCount, 2);
      assert.equal(schedulerRunId, latest.run_id);
      assert.equal(laneState.status, 'pending');
      assert.deepEqual(laneState.invalidation_concern_match?.matched_files, ['docs/openapi/admin.yaml']);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('replays unseen scheduler events before the next pass on restart', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-scheduler-replay-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const runRoot = join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime');
      const schedulerPath = join(runRoot, 'scheduler-state.json');
      const eventsPath = join(runRoot, 'events.jsonl');
      const counterPath = join(runRoot, 'event-counter.json');
      const laneStatePath = join(runRoot, 'security-reviewer.json');
      const schedulerState = JSON.parse(await readFile(schedulerPath, 'utf-8')) as { last_processed_event_id: number; replay_count?: number; last_completed_pass_id?: number };
      const eventCounter = JSON.parse(await readFile(counterPath, 'utf-8')) as { next_id: number };
      const laneState = JSON.parse(await readFile(laneStatePath, 'utf-8')) as Record<string, unknown>;
      const replayEventId = eventCounter.next_id;

      await writeFile(eventsPath, `${JSON.stringify({
        id: replayEventId,
        type: 'lane_invalidated',
        lane_id: laneState.lane_id,
        alias: 'security-reviewer',
        role: 'security-reviewer',
        at: now.toISOString(),
        changed_files: ['config/security.yml'],
        concern_match: {
          concern_key: 'security',
          matched_files: ['config/security.yml'],
          direct_files: ['config/security.yml'],
          fallback_files: [],
          unknown_files: [],
          unmatched_files: [],
          reasons: [{ file: 'config/security.yml', kind: 'direct', evidence: 'test fixture' }],
        },
      })}\n`, { encoding: 'utf-8', flag: 'a' });
      await writeFile(counterPath, JSON.stringify({ next_id: replayEventId + 1 }, null, 2), 'utf-8');
      await writeFile(laneStatePath, JSON.stringify({
        ...laneState,
        status: 'pending',
        invalidated_at: now.toISOString(),
        invalidated_reason: 'Concern-relevant files changed after completion: config/security.yml',
      }, null, 2), 'utf-8');
      await writeFile(schedulerPath, JSON.stringify({
        ...schedulerState,
        last_processed_event_id: replayEventId - 1,
      }, null, 2), 'utf-8');

      const result = await continueGithubSchedulerLoop({
        runId: latest.run_id,
        env,
        homeDir,
        fetchImpl: createFetchStub({
          '/user': { login: 'dkropachev' },
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/issues/42': {
            number: 42,
            title: 'Implement queue healing',
            body: 'Need to keep workers alive after review updates.',
            html_url: 'https://github.com/acme/widget/issues/42',
            state: 'open',
            updated_at: '2026-04-03T09:00:00.000Z',
            user: { login: 'requester' },
          },
          [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
          '/repos/acme/widget/issues/42/comments?per_page=100': [],
        }),
        runLaneProcess: async ({ lane, runPaths }) => {
          const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
          await mkdir(dirname(resultPath), { recursive: true });
          await writeFile(resultPath, `${lane.alias} replayed\n`, 'utf-8');
          return {
            output: `${lane.alias} replayed\n`,
            resultPath,
            laneCodexHome: 'test://lane',
            status: 'completed' as const,
          };
        },
        launchWithHud: async () => {},
        writeLine: () => {},
      });

      const updatedSchedulerState = JSON.parse(await readFile(schedulerPath, 'utf-8')) as { replay_count?: number; last_processed_event_id: number };
      const updatedLaneState = JSON.parse(await readFile(laneStatePath, 'utf-8')) as { status: string };
      assert.equal(result.hasRemainingWork, false);
      assert.equal(updatedLaneState.status, 'completed');
      assert.equal(updatedSchedulerState.replay_count, 1);
      assert.equal(updatedSchedulerState.last_processed_event_id >= replayEventId, true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('recovers stale leader and publisher sessions before declaring scheduler completion', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-session-recovery-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--considerations', 'security'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          runLaneProcess: async ({ lane, runPaths }) => {
            const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
            await mkdir(dirname(resultPath), { recursive: true });
            await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
            return {
              output: `${lane.alias} findings\n`,
              resultPath,
              laneCodexHome: 'test://lane',
              status: 'completed' as const,
            };
          },
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const runRoot = join(managedRepoRoot, 'runs', latest.run_id);
      const laneRuntimeRoot = join(runRoot, 'lane-runtime');
      const leaderStatusPath = join(laneRuntimeRoot, 'leader-status.json');
      const publisherStatusPath = join(laneRuntimeRoot, 'publisher-status.json');

      await writeFile(leaderStatusPath, JSON.stringify({
        run_id: latest.run_id,
        session_active: true,
        pid: 999999,
        bootstrap_complete: true,
        implementation_started: true,
        implementation_complete: true,
        ready_for_publication: true,
        blocked: false,
        updated_at: now.toISOString(),
      }, null, 2), 'utf-8');
      await writeFile(publisherStatusPath, JSON.stringify({
        run_id: latest.run_id,
        session_active: true,
        pid: 999998,
        started: false,
        pr_opened: false,
        ci_waiting: false,
        ci_green: false,
        blocked: false,
        recovery_count: 0,
        milestones: [],
        updated_at: now.toISOString(),
      }, null, 2), 'utf-8');

      const result = await continueGithubSchedulerLoop({
        runId: latest.run_id,
        env,
        homeDir,
        fetchImpl: createFetchStub({
          '/user': { login: 'dkropachev' },
          '/repos/acme/widget': {
            name: 'widget',
            full_name: repoSlug,
            clone_url: barePath,
            default_branch: 'main',
            html_url: 'https://github.com/acme/widget',
          },
          '/repos/acme/widget/issues/42': {
            number: 42,
            title: 'Implement queue healing',
            body: 'Need to keep workers alive after review updates.',
            html_url: 'https://github.com/acme/widget/issues/42',
            state: 'open',
            updated_at: '2026-04-03T09:00:00.000Z',
            user: { login: 'requester' },
          },
          [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
          '/repos/acme/widget/issues/42/comments?per_page=100': [],
        }),
        runLaneProcess: async ({ lane, runPaths }) => {
          const resultPath = join(runPaths.runDir, 'lane-runtime', `${lane.alias}-result.md`);
          await mkdir(dirname(resultPath), { recursive: true });
          await writeFile(resultPath, `${lane.alias} findings\n`, 'utf-8');
          return {
            output: `${lane.alias} findings\n`,
            resultPath,
            laneCodexHome: 'test://lane',
            status: 'completed' as const,
          };
        },
        launchWithHud: async () => {},
        writeLine: () => {},
      });

      const leaderStatus = JSON.parse(await readFile(leaderStatusPath, 'utf-8')) as { session_active?: boolean; pid?: number };
      const publisherStatus = JSON.parse(await readFile(publisherStatusPath, 'utf-8')) as { session_active?: boolean; pid?: number };
      const report = await checkGithubWorkonRuntimeConsistency({
        manifest: JSON.parse(await readFile(join(runRoot, 'manifest.json'), 'utf-8')),
        runPaths: {
          runDir: runRoot,
          manifestPath: join(runRoot, 'manifest.json'),
          startInstructionsPath: join(runRoot, 'start-instructions.md'),
          feedbackInstructionsPath: join(runRoot, 'feedback-instructions.md'),
        },
      });
      const schedulerState = JSON.parse(await readFile(join(laneRuntimeRoot, 'scheduler-state.json'), 'utf-8')) as { recovery_count?: number };

      assert.equal(result.hasRemainingWork, false);
      assert.equal(leaderStatus.session_active, false);
      assert.equal(publisherStatus.session_active, false);
      assert.equal(leaderStatus.pid, undefined);
      assert.equal(publisherStatus.pid, undefined);
      assert.equal((schedulerState.recovery_count ?? 0) >= 2, true);
      assert.equal(report.ok, true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('reuses an existing draft PR and finishes immediately when CI is already green for the current head', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-publisher-recovery-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
        NANA_GITHUB_CI_POLL_INTERVAL_MS: '1',
        NANA_GITHUB_CI_TIMEOUT_MS: '1',
      } as NodeJS.ProcessEnv;
      let daemonRunId = '';
      let repoCheckoutPath = '';
      let startWorkflowPolls = 0;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42', '--create-pr'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: ({ runId }) => {
            daemonRunId = runId;
          },
          fetchImpl: async (input, init) => {
            const url = typeof input === 'string'
              ? new URL(input)
              : input instanceof URL
                ? input
                : new URL(input.url);
            const key = `${url.pathname}${url.search}`;
            const method = (init?.method || 'GET').toUpperCase();
            if (method === 'GET' && key === '/user') return jsonResponse({ login: 'dkropachev' });
            if (method === 'GET' && key === '/repos/acme/widget') {
              return jsonResponse({
                name: 'widget',
                full_name: repoSlug,
                clone_url: barePath,
                default_branch: 'main',
                html_url: 'https://github.com/acme/widget',
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
              return jsonResponse({
                number: 42,
                title: 'Implement queue healing',
                body: 'Need to keep workers alive after review updates.',
                html_url: 'https://github.com/acme/widget/issues/42',
                state: 'open',
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
              });
            }
            if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
              return jsonResponse([]);
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
              return jsonResponse([]);
            }
            if (method === 'POST' && key === '/repos/acme/widget/pulls') {
              return jsonResponse({
                number: 91,
                title: 'Implement queue healing',
                body: 'body',
                html_url: 'https://github.com/acme/widget/pull/91',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T10:02:00.000Z',
                user: { login: 'dkropachev' },
                head: { ref: issueBranch, sha: 'placeholder', repo: { full_name: repoSlug } },
                base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/commits/') && key.endsWith('/check-runs?per_page=100')) {
              return jsonResponse({
                total_count: 0,
                check_runs: [],
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
              startWorkflowPolls += 1;
              const sha = execFileSync('git', ['rev-parse', 'HEAD'], {
                cwd: repoCheckoutPath,
                encoding: 'utf-8',
                stdio: ['ignore', 'pipe', 'pipe'],
              }).trim();
              return jsonResponse({
                total_count: 1,
                workflow_runs: [{ id: 77, name: 'CI', head_sha: sha, status: 'in_progress', conclusion: null, html_url: 'https://example.invalid/actions/77' }],
              });
            }
            return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
          },
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch\n', 'utf-8');
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      let patchCalls = 0;
      let postCalls = 0;

      const result = await continueGithubSchedulerLoop({
        runId: latest.run_id,
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        fetchImpl: async (input, init) => {
          const url = typeof input === 'string'
            ? new URL(input)
            : input instanceof URL
              ? input
              : new URL(input.url);
          const key = `${url.pathname}${url.search}`;
          const method = (init?.method || 'GET').toUpperCase();
          const currentHeadSha = execFileSync('git', ['rev-parse', 'HEAD'], {
            cwd: repoCheckoutPath,
            encoding: 'utf-8',
            stdio: ['ignore', 'pipe', 'pipe'],
          }).trim();
          if (method === 'GET' && key === '/user') return jsonResponse({ login: 'dkropachev' });
          if (method === 'GET' && key === '/repos/acme/widget') {
            return jsonResponse({
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            });
          }
          if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
            return jsonResponse({
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            });
          }
          if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
            return jsonResponse([{
              number: 91,
              title: 'Implement queue healing',
              body: 'body',
              html_url: 'https://github.com/acme/widget/pull/91',
              state: 'open',
              merged_at: null,
              updated_at: '2026-04-03T10:05:00.000Z',
              user: { login: 'dkropachev' },
              head: { ref: issueBranch, sha: currentHeadSha, repo: { full_name: repoSlug } },
              base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
            }]);
          }
          if (method === 'PATCH' && key === '/repos/acme/widget/pulls/91') {
            patchCalls += 1;
            return jsonResponse({
              number: 91,
              title: 'Implement queue healing',
              body: 'updated body',
              html_url: 'https://github.com/acme/widget/pull/91',
              state: 'open',
              merged_at: null,
              updated_at: '2026-04-03T10:06:00.000Z',
              user: { login: 'dkropachev' },
              head: { ref: issueBranch, sha: currentHeadSha, repo: { full_name: repoSlug } },
              base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
            });
          }
          if (method === 'POST' && key === '/repos/acme/widget/pulls') {
            postCalls += 1;
            return new Response('unexpected create', { status: 500 });
          }
          if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
            return jsonResponse([]);
          }
          if (method === 'GET' && key.startsWith('/repos/acme/widget/commits/') && key.endsWith('/check-runs?per_page=100')) {
            return jsonResponse({
              total_count: 1,
              check_runs: [{ id: 1, name: 'lint', status: 'completed', conclusion: 'success', html_url: 'https://example.invalid/check/1' }],
            });
          }
          if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
            return jsonResponse({
              total_count: 1,
              workflow_runs: [{ id: 77, name: 'CI', head_sha: currentHeadSha, status: 'completed', conclusion: 'success', html_url: 'https://example.invalid/actions/77' }],
            });
          }
          if (method === 'GET' && key === '/repos/acme/widget/actions/runs/77/jobs?per_page=100') {
            return jsonResponse({
              total_count: 1,
              jobs: [{ id: 12, name: 'CI', started_at: '2026-04-03T10:00:00.000Z', completed_at: '2026-04-03T10:01:00.000Z', conclusion: 'success' }],
            });
          }
          return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
        },
        launchWithHud: async () => {},
        writeLine: () => {},
      });

      const manifest = JSON.parse(await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'manifest.json'), 'utf-8')) as {
        publication_state?: string;
      };
      const publisherStatus = JSON.parse(
        await readFile(join(managedRepoRoot, 'runs', latest.run_id, 'lane-runtime', 'publisher-status.json'), 'utf-8'),
      ) as { recovery_count?: number; last_milestone?: string };

      assert.equal(daemonRunId, latest.run_id);
      assert.equal(startWorkflowPolls > 0, true);
      assert.equal(result.hasRemainingWork, false);
      assert.equal(manifest.publication_state, 'ci_green');
      assert.equal(postCalls, 0);
      assert.equal(patchCalls, 1);
      assert.equal((publisherStatus.recovery_count ?? 0) >= 1, true);
      assert.equal(publisherStatus.last_milestone, 'ci_green');
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('does not resume blocked publication when publisher status marks the current head as non-retryable', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-publisher-blocked-status-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = {
        ...process.env,
        GH_TOKEN: 'test-token',
      } as NodeJS.ProcessEnv;
      let repoCheckoutPath = '';

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: () => {},
          fetchImpl: async (input, init) => {
            const url = typeof input === 'string'
              ? new URL(input)
              : input instanceof URL
                ? input
                : new URL(input.url);
            const key = `${url.pathname}${url.search}`;
            const method = (init?.method || 'GET').toUpperCase();
            if (method === 'GET' && key === '/user') return jsonResponse({ login: 'dkropachev' });
            if (method === 'GET' && key === '/repos/acme/widget') {
              return jsonResponse({
                name: 'widget',
                full_name: repoSlug,
                clone_url: barePath,
                default_branch: 'main',
                html_url: 'https://github.com/acme/widget',
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
              return jsonResponse({
                number: 42,
                title: 'Implement queue healing',
                body: 'Need to keep workers alive after review updates.',
                html_url: 'https://github.com/acme/widget/issues/42',
                state: 'open',
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
              });
            }
            if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
              return jsonResponse([]);
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
              return jsonResponse([]);
            }
            return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
          },
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch\n', 'utf-8');
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const runRoot = join(managedRepoRoot, 'runs', latest.run_id);
      const manifestPath = join(runRoot, 'manifest.json');
      const leaderStatusPath = join(runRoot, 'lane-runtime', 'leader-status.json');
      const publisherStatusPath = join(runRoot, 'lane-runtime', 'publisher-status.json');
      const publisherLaneStatePath = join(runRoot, 'lane-runtime', 'publisher.json');
      const manifest = JSON.parse(await readFile(manifestPath, 'utf-8')) as Record<string, unknown>;
      const currentHeadSha = execFileSync('git', ['rev-parse', 'HEAD'], {
        cwd: repoCheckoutPath,
        encoding: 'utf-8',
        stdio: ['ignore', 'pipe', 'pipe'],
      }).trim();

      await writeFile(manifestPath, JSON.stringify({
        ...manifest,
        create_pr_on_complete: true,
        published_pr_number: 91,
        published_pr_url: 'https://github.com/acme/widget/pull/91',
        published_pr_head_ref: issueBranch,
        publication_state: 'blocked',
        publication_error: 'opaque legacy failure string',
      }, null, 2), 'utf-8');
      await writeFile(publisherLaneStatePath, JSON.stringify({
        version: 1,
        lane_id: `${latest.run_id}:publisher`,
        alias: 'publisher',
        role: 'publisher',
        profile: 'publisher',
        activation: 'publication',
        phase: 'publication',
        blocking: false,
        depends_on: [],
        status: 'failed',
        retry_count: 0,
        retryable: true,
        retry_policy: 'publisher-recovery',
        retry_exhausted: false,
        updated_at: now.toISOString(),
        completed_at: now.toISOString(),
        instructions_path: join(runRoot, 'lane-runtime', 'publisher-inbox.md'),
        result_path: join(runRoot, 'lane-runtime', 'publisher-result.md'),
        stdout_path: join(runRoot, 'lane-runtime', 'publisher-stdout.log'),
        stderr_path: join(runRoot, 'lane-runtime', 'publisher-stderr.log'),
      }, null, 2), 'utf-8');
      await writeFile(leaderStatusPath, JSON.stringify({
        run_id: latest.run_id,
        session_active: false,
        bootstrap_complete: true,
        implementation_started: true,
        implementation_complete: true,
        ready_for_publication: false,
        blocked: false,
        updated_at: now.toISOString(),
      }, null, 2), 'utf-8');
      await writeFile(publisherStatusPath, JSON.stringify({
        run_id: latest.run_id,
        session_active: false,
        started: true,
        pr_opened: true,
        ci_waiting: false,
        ci_green: false,
        blocked: true,
        blocked_reason: 'Checks failed after automatic rerun attempts were exhausted.',
        blocked_reason_category: 'ci_failed_checks',
        blocked_retryable: false,
        current_head_sha: currentHeadSha,
        current_branch: issueBranch,
        current_pr_number: 91,
        last_milestone: 'ci_blocked',
        milestones: [{
          milestone: 'ci_blocked',
          at: now.toISOString(),
          head_sha: currentHeadSha,
          pr_number: 91,
        }],
        updated_at: now.toISOString(),
      }, null, 2), 'utf-8');

      let fetchedActionRuns = 0;
      const result = await continueGithubSchedulerLoop({
        runId: latest.run_id,
        env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
        homeDir,
        fetchImpl: async (input) => {
          const url = typeof input === 'string'
            ? new URL(input)
            : input instanceof URL
              ? input
              : new URL(input.url);
          const key = `${url.pathname}${url.search}`;
          if (key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) fetchedActionRuns += 1;
          if (key === '/user') return jsonResponse({ login: 'dkropachev' });
          if (key === '/repos/acme/widget') {
            return jsonResponse({
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            });
          }
          if (key === '/repos/acme/widget/issues/42') {
            return jsonResponse({
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            });
          }
          if (key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
            return jsonResponse([]);
          }
          if (key === '/repos/acme/widget/issues/42/comments?per_page=100') {
            return jsonResponse([]);
          }
          return new Response(`unexpected route: ${key}`, { status: 500 });
        },
        launchWithHud: async () => {},
        writeLine: () => {},
      });

      assert.equal(result.manifest.publication_state, 'blocked');
      assert.equal(fetchedActionRuns, 0);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('reports linked PR target and CI status when syncing an issue-owned run through a PR URL with no new feedback', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-sync-pr-status-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubReviewRulesCommand(['config', 'set', '--mode', 'automatic'], {
        env,
        homeDir,
        now: () => now,
        writeLine: () => {},
      });
      let repoCheckoutPath = '';

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch\n', 'utf-8');
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const runRoot = join(managedRepoRoot, 'runs', latest.run_id);
      const manifestPath = join(runRoot, 'manifest.json');
      const manifest = JSON.parse(await readFile(manifestPath, 'utf-8')) as Record<string, unknown>;
      const currentHeadSha = execFileSync('git', ['rev-parse', 'HEAD'], {
        cwd: repoCheckoutPath,
        encoding: 'utf-8',
        stdio: ['ignore', 'pipe', 'pipe'],
      }).trim();

      await writeFile(manifestPath, JSON.stringify({
        ...manifest,
        create_pr_on_complete: true,
        published_pr_number: 6,
        published_pr_url: 'https://github.com/acme/widget/pull/6',
        published_pr_head_ref: issueBranch,
        publication_state: 'ci_waiting',
      }, null, 2), 'utf-8');

      const lines: string[] = [];
      await githubCommand(
        ['sync', '--run-id', latest.run_id, 'https://github.com/acme/widget/pull/6'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: async (input, init) => {
            const url = typeof input === 'string'
              ? new URL(input)
              : input instanceof URL
                ? input
                : new URL(input.url);
            const key = `${url.pathname}${url.search}`;
            const method = (init?.method || 'GET').toUpperCase();
            if (method === 'GET' && key === '/user') return jsonResponse({ login: 'dkropachev' });
            if (method === 'GET' && key === '/repos/acme/widget') {
              return jsonResponse({
                name: 'widget',
                full_name: repoSlug,
                clone_url: barePath,
                default_branch: 'main',
                html_url: 'https://github.com/acme/widget',
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
              return jsonResponse({
                number: 42,
                title: 'Implement queue healing',
                body: 'Need to keep workers alive after review updates.',
                html_url: 'https://github.com/acme/widget/issues/42',
                state: 'open',
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/issues/42/comments?per_page=100') {
              return jsonResponse([]);
            }
            if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
              return jsonResponse([{
                number: 6,
                title: 'Implement queue healing',
                body: 'body',
                html_url: 'https://github.com/acme/widget/pull/6',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T10:02:00.000Z',
                user: { login: 'dkropachev' },
                head: { ref: issueBranch, sha: currentHeadSha, repo: { full_name: repoSlug } },
                base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
              }]);
            }
            if (method === 'GET' && key === '/repos/acme/widget/pulls/6') {
              return jsonResponse({
                number: 6,
                title: 'Implement queue healing',
                body: 'body',
                html_url: 'https://github.com/acme/widget/pull/6',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T10:02:00.000Z',
                user: { login: 'dkropachev' },
                head: { ref: issueBranch, sha: currentHeadSha, repo: { full_name: repoSlug } },
                base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
              });
            }
            if (method === 'GET' && key === '/repos/acme/widget/pulls/6/reviews?per_page=100') {
              return jsonResponse([
                {
                  id: 601,
                  html_url: 'https://github.com/acme/widget/pull/6#pullrequestreview-601',
                  body: 'Please add regression tests for this behavior change before merge.',
                  submitted_at: '2026-04-02T12:00:00.000Z',
                  state: 'CHANGES_REQUESTED',
                  user: { login: 'reviewer-a' },
                },
                {
                  id: 602,
                  html_url: 'https://github.com/acme/widget/pull/6#pullrequestreview-602',
                  body: 'Needs regression coverage before we merge this.',
                  submitted_at: '2026-04-02T13:00:00.000Z',
                  state: 'COMMENTED',
                  user: { login: 'reviewer-b' },
                },
              ]);
            }
            if (method === 'GET' && key === '/repos/acme/widget/pulls/6/comments?per_page=100') {
              return jsonResponse([]);
            }
            if (method === 'GET' && key.startsWith(`/repos/acme/widget/commits/${currentHeadSha}/check-runs`)) {
              return jsonResponse({
                total_count: 0,
                check_runs: [],
              });
            }
            if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
              return jsonResponse({
                total_count: 1,
                workflow_runs: [{ id: 77, name: 'CI', head_sha: currentHeadSha, status: 'in_progress', conclusion: null, html_url: 'https://example.invalid/actions/77' }],
              });
            }
            return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
          },
          launchWithHud: async () => {
            throw new Error('launchWithHud should not run when there is no new feedback');
          },
          writeLine: (line) => lines.push(line),
        },
      );

      const output = lines.join('\n');
      assert.match(output, /No new feedback from @dkropachev for acme\/widget issue #42 \(linked PR #6\)\./i);
      assert.match(output, /CI\/publication status for PR #6: blocked\./i);
      assert.match(output, /Review-rules automatic refresh for acme\/widget: approved=1 pending=0\./i);
      const rulesPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'source', '.nana', 'repo-review-rules.json');
      const rulesDoc = JSON.parse(await readFile(rulesPath, 'utf-8')) as { approved_rules: Array<{ category: string }> };
      assert.equal(rulesDoc.approved_rules.some((rule) => rule.category === 'qa'), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('skips push when the publication branch is already pushed for the current head', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-branch-already-pushed-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let repoCheckoutPath = '';

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          startSchedulerDaemon: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            repoCheckoutPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId, 'repo');
            await writeFile(join(repoCheckoutPath, 'feature.txt'), 'feature branch\n', 'utf-8');
          },
          writeLine: () => {},
        },
      );

      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const latest = JSON.parse(await readFile(join(managedRepoRoot, 'latest-run.json'), 'utf-8')) as { run_id: string };
      const runRoot = join(managedRepoRoot, 'runs', latest.run_id);
      const manifestPath = join(runRoot, 'manifest.json');
      const manifest = JSON.parse(await readFile(manifestPath, 'utf-8')) as Record<string, unknown>;
      const fakeBin = join(wd, 'fake-bin');
      await mkdir(fakeBin, { recursive: true });
      await writeFile(join(fakeBin, 'nana'), '#!/bin/sh\nexit 0\n', 'utf-8');
      execFileSync('chmod', ['+x', join(fakeBin, 'nana')], { stdio: 'ignore' });
      const currentHeadSha = execFileSync('git', ['rev-parse', 'HEAD'], {
        cwd: repoCheckoutPath,
        encoding: 'utf-8',
        stdio: ['ignore', 'pipe', 'pipe'],
      }).trim();

      execFileSync('git', ['add', 'feature.txt'], { cwd: repoCheckoutPath, stdio: 'ignore' });
      execFileSync('git', ['commit', '-m', 'ready-for-publication'], { cwd: repoCheckoutPath, stdio: 'ignore' });

      const publishedHeadSha = execFileSync('git', ['rev-parse', 'HEAD'], {
        cwd: repoCheckoutPath,
        encoding: 'utf-8',
        stdio: ['ignore', 'pipe', 'pipe'],
      }).trim();

      execFileSync('git', ['push', '--set-upstream', 'origin', `${issueBranch}:${issueBranch}`], {
        cwd: repoCheckoutPath,
        stdio: ['ignore', 'pipe', 'pipe'],
      });

      await writeFile(manifestPath, JSON.stringify({
        ...manifest,
        create_pr_on_complete: true,
        publication_state: 'not_started',
      }, null, 2), 'utf-8');

      const updated = await continueGithubPublicationLoop({
        runId: latest.run_id,
        env: {
          ...env,
          PATH: env.PATH ? `${fakeBin}:${env.PATH}` : fakeBin,
        },
        homeDir,
        fetchImpl: async (input, init) => {
          const url = typeof input === 'string'
            ? new URL(input)
            : input instanceof URL
              ? input
              : new URL(input.url);
          const key = `${url.pathname}${url.search}`;
          const method = (init?.method || 'GET').toUpperCase();
          if (method === 'GET' && key === '/repos/acme/widget') {
            return jsonResponse({
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            });
          }
          if (method === 'GET' && key === '/repos/acme/widget/issues/42') {
            return jsonResponse({
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            });
          }
          if (method === 'GET' && key === `/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`) {
            return jsonResponse([]);
          }
          if (method === 'POST' && key === '/repos/acme/widget/pulls') {
            return jsonResponse({
              number: 91,
              title: 'Implement queue healing',
              body: 'body',
              html_url: 'https://github.com/acme/widget/pull/91',
              state: 'open',
              merged_at: null,
              updated_at: '2026-04-03T10:02:00.000Z',
              user: { login: 'dkropachev' },
              head: { ref: issueBranch, sha: publishedHeadSha, repo: { full_name: repoSlug } },
              base: { ref: 'main', sha: 'base-sha', repo: { full_name: repoSlug } },
            });
          }
          if (method === 'GET' && key.startsWith('/repos/acme/widget/commits/') && key.endsWith('/check-runs?per_page=100')) {
            return jsonResponse({
              total_count: 1,
              check_runs: [{ id: 1, name: 'lint', status: 'completed', conclusion: 'success', html_url: 'https://example.invalid/check/1' }],
            });
          }
          if (method === 'GET' && key.startsWith('/repos/acme/widget/actions/runs?head_sha=')) {
            return jsonResponse({
              total_count: 1,
              workflow_runs: [{ id: 77, name: 'CI', head_sha: publishedHeadSha, status: 'completed', conclusion: 'success', html_url: 'https://example.invalid/actions/77' }],
            });
          }
          if (method === 'GET' && key === '/repos/acme/widget/actions/runs/77/jobs?per_page=100') {
            return jsonResponse({
              total_count: 1,
              jobs: [{ id: 12, name: 'CI', started_at: '2026-04-03T10:00:00.000Z', completed_at: '2026-04-03T10:01:00.000Z', conclusion: 'success' }],
            });
          }
          return new Response(`unexpected route: ${method} ${key}`, { status: 500 });
        },
        writeLine: () => {},
      });

      const publisherStatus = JSON.parse(
        await readFile(join(runRoot, 'lane-runtime', 'publisher-status.json'), 'utf-8'),
      ) as { milestones?: Array<{ milestone?: string; detail?: string }> };
      const pushMilestone = (publisherStatus.milestones ?? []).find((milestone) => milestone.milestone === 'push_completed');

      assert.equal(updated.publication_state, 'ci_green');
      assert.equal(pushMilestone?.detail, 'branch already pushed');
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('uses saved default considerations for a repo when start has no considerations flag', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-default-considerations-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      await githubCommand(
        ['defaults', 'set', 'acme/widget', '--considerations', 'security,dependency'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          now: () => now,
          writeLine: () => {},
        },
      );

      let appendixPath = '';
      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            if (!appendixPath) appendixPath = env[GITHUB_APPEND_ENV] || '';
          },
          writeLine: () => {},
        },
      );

      const appendix = await readFile(appendixPath, 'utf-8');
      assert.doesNotMatch(appendix, /Execution mode:/i);
      assert.match(appendix, /Active considerations: security, dependency/i);
      assert.match(appendix, /coder -> executor \[execute, owner=self, blocking\]/i);
      assert.match(appendix, /dependency-expert -> dependency-expert \[review, owner=coder, blocking\]/i);
      assert.match(appendix, /security-reviewer -> security-reviewer \[review, owner=coder, blocking\]/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('shows the resolved default pipeline for a repo', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-defaults-show-'));
    const homeDir = join(wd, 'home');
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const lines: string[] = [];
      await githubCommand(
        ['defaults', 'set', 'acme/widget', '--considerations', 'style,qa,security'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          now: () => now,
          writeLine: () => {},
        },
      );

      await githubCommand(
        ['defaults', 'show', 'acme/widget'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          writeLine: (line) => lines.push(line),
        },
      );

      const output = lines.join('\n');
      assert.match(output, /Default considerations for acme\/widget: style, qa, security/i);
      assert.match(output, /Default role layout for acme\/widget: split/i);
      assert.match(output, /Repo review-rules mode for acme\/widget: \(none\)/i);
      assert.match(output, /Effective review-rules mode for acme\/widget: manual/i);
      assert.match(output, /Resolved default pipeline:/i);
      assert.match(output, /Active considerations: style, qa, security/i);
      assert.match(output, /Role layout: split/i);
      assert.match(output, /coder -> executor \[execute, owner=self, blocking\]/i);
      assert.match(output, /test-engineer -> test-engineer \[execute, owner=self, blocking\]/i);
      assert.match(output, /style-reviewer -> style-reviewer \[review, owner=coder, advisory\]/i);
      assert.match(output, /security-reviewer -> security-reviewer \[review, owner=coder, blocking\]/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('shows merged reviewer+executor role layout from saved defaults', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-defaults-show-merged-role-layout-'));
    const homeDir = join(wd, 'home');
    const now = new Date('2026-04-03T10:00:00.000Z');
    try {
      const lines: string[] = [];
      await githubCommand(
        ['defaults', 'set', 'acme/widget', '--considerations', 'security', '--role-layout', 'reviewer+executor', '--review-rules-mode', 'automatic'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          now: () => now,
          writeLine: () => {},
        },
      );

      await githubCommand(
        ['defaults', 'show', 'acme/widget'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          writeLine: (line) => lines.push(line),
        },
      );

      const output = lines.join('\n');
      assert.match(output, /Default role layout for acme\/widget: reviewer\+executor/i);
      assert.match(output, /Repo review-rules mode for acme\/widget: automatic/i);
      assert.match(output, /Effective review-rules mode for acme\/widget: automatic/i);
      assert.match(output, /Role layout: reviewer\+executor/i);
      assert.match(output, /security-reviewer -> security-reviewer\+executor \[review\+execute, owner=self, blocking\]/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });


  it('tracks cumulative issue token usage and reports it via stats', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-issue-token-stats-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: repoSlug,
              clone_url: barePath,
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [],
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
          }),
          launchWithHud: async () => {
            await mkdir(join(process.cwd(), '.nana'), { recursive: true });
            await writeFile(join(process.cwd(), '.nana', 'metrics.json'), JSON.stringify({
              total_turns: 3,
              session_turns: 3,
              last_activity: '2026-04-03T10:10:00.000Z',
              session_input_tokens: 120,
              session_output_tokens: 80,
              session_total_tokens: 200,
            }, null, 2));
          },
          writeLine: () => {},
        },
      );

      const lines: string[] = [];
      await githubCommand(
        ['stats', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => new Date('2026-04-03T10:15:00.000Z'),
          writeLine: (line) => lines.push(line),
        },
      );

      const output = lines.join('\n');
      assert.match(output, /Token stats for acme\/widget issue #42/i);
      assert.match(output, /Total input tokens: 120/i);
      assert.match(output, /Total output tokens: 80/i);
      assert.match(output, /Total tokens: 200/i);
      assert.match(output, /Sessions accounted: 1/i);
      assert.match(output, new RegExp(`- ${issueSandboxId}: total=200 input=120 output=80 sessions=1`, 'i'));
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('renders a retrospective report for the latest run', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-retrospective-'));
    const homeDir = join(wd, 'home');
    const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
    const sandboxPath = join(managedRepoRoot, 'sandboxes', 'issue-42-pr-123456789012');
    const repoCheckoutPath = join(sandboxPath, 'repo');
    const runId = 'gh-retro-1';
    try {
      await mkdir(join(sandboxPath, '.nana'), { recursive: true });
      const sessionsDir = join(sandboxPath, '.codex', 'sessions', '2026', '04', '03');
      await mkdir(sessionsDir, { recursive: true });
      await mkdir(repoCheckoutPath, { recursive: true });
      await writeFile(join(repoCheckoutPath, 'README.md'), '# widget\n', 'utf-8');
      initGitRepo(repoCheckoutPath);
      await writeFile(join(sessionsDir, 'rollout-1.jsonl'), [
        JSON.stringify({
          timestamp: '2026-04-03T17:00:01.000Z',
          type: 'session_meta',
          payload: { agent_nickname: '', agent_role: '' },
        }),
        JSON.stringify({
          timestamp: '2026-04-03T17:00:11.000Z',
          type: 'event_msg',
          payload: { type: 'token_count', info: { total_token_usage: { total_tokens: 1234 } } },
        }),
      ].join('\n') + '\n', 'utf-8');
      await writeFile(join(sessionsDir, 'rollout-2.jsonl'), [
        JSON.stringify({
          timestamp: '2026-04-03T17:00:02.000Z',
          type: 'session_meta',
          payload: { agent_nickname: 'Gauss', agent_role: 'architect' },
        }),
        JSON.stringify({
          timestamp: '2026-04-03T17:00:09.000Z',
          type: 'event_msg',
          payload: { type: 'token_count', info: { total_token_usage: { total_tokens: 4321 } } },
        }),
      ].join('\n') + '\n', 'utf-8');
      await writeFile(join(sandboxPath, '.nana', 'sandbox.json'), JSON.stringify({
        version: 1,
        sandbox_id: 'issue-42-pr-123456789012',
        repo_slug: 'acme/widget',
        repo_name: 'widget',
        sandbox_path: sandboxPath,
        repo_checkout_path: repoCheckoutPath,
        branch_name: 'nana/issue-42/issue-42-pr-123456789012',
        base_ref: 'origin/main',
        target_kind: 'issue',
        target_number: 42,
        created_at: '2026-04-03T10:00:00.000Z',
        updated_at: '2026-04-03T10:00:00.000Z',
      }, null, 2));
      await mkdir(join(managedRepoRoot, 'runs', runId), { recursive: true });
      await mkdir(join(homeDir, '.nana', 'github-workon'), { recursive: true });
      await writeFile(join(homeDir, '.nana', 'github-workon', 'latest-run.json'), JSON.stringify({
        repo_root: managedRepoRoot,
        run_id: runId,
      }, null, 2));
      await writeFile(join(managedRepoRoot, 'runs', runId, 'manifest.json'), JSON.stringify({
        version: 3,
        run_id: runId,
        created_at: '2026-04-03T10:00:00.000Z',
        updated_at: '2026-04-03T10:10:00.000Z',
        repo_slug: 'acme/widget',
        repo_owner: 'acme',
        repo_name: 'widget',
        managed_repo_root: managedRepoRoot,
        source_path: join(managedRepoRoot, 'source'),
        sandbox_id: 'issue-42-pr-123456789012',
        sandbox_path: sandboxPath,
        sandbox_repo_path: repoCheckoutPath,
        considerations_active: ['arch', 'qa'],
        role_layout: 'split',
        consideration_pipeline: [
          {
            alias: 'architect',
            role: 'architect',
            prompt_roles: ['architect'],
            activation: 'bootstrap',
            phase: 'pre-impl',
            mode: 'review',
            owner: 'coder',
            blocking: true,
            purpose: 'Review design boundaries.',
          },
          {
            alias: 'coder',
            role: 'executor',
            prompt_roles: ['executor'],
            activation: 'bootstrap',
            phase: 'impl',
            mode: 'execute',
            owner: 'self',
            blocking: true,
            purpose: 'Primary implementation lane.',
          },
        ],
        lane_prompt_artifacts: [],
        team_resolved_aliases: ['architect', 'coder'],
        team_resolved_roles: ['architect', 'executor'],
        create_pr_on_complete: false,
        issue_association_number: 42,
        target_kind: 'issue',
        target_number: 42,
        target_title: 'Implement queue healing',
        target_url: 'https://github.com/acme/widget/issues/42',
        target_state: 'open',
        review_reviewer: 'dkropachev',
        api_base_url: 'https://api.github.com',
        default_branch: 'main',
        last_seen_issue_comment_id: 0,
        last_seen_review_id: 0,
        last_seen_review_comment_id: 0,
      }, null, 2));

      const lines: string[] = [];
      await githubCommand(
        ['retrospective', '--last'],
        {
          env: { ...process.env } as NodeJS.ProcessEnv,
          homeDir,
          writeLine: (line) => lines.push(line),
        },
      );

      const output = lines.join('\n');
      assert.match(output, /NANA Work-on Retrospective/i);
      assert.match(output, /Role layout: split/i);
      assert.match(output, /Total thread tokens: 5555/i);
      assert.match(output, /Gauss: role=architect class=reviewer tokens=4321/i);
      assert.match(output, /Efficiency Findings/i);
      assert.match(output, /Missing Features \/ Angles To Investigate/i);
      assert.match(output, /Different Angles/i);
      assert.equal(existsSync(join(managedRepoRoot, 'runs', runId, 'thread-usage.json')), true);
      assert.equal(existsSync(join(managedRepoRoot, 'runs', runId, 'retrospective.md')), true);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('fails when the target-owned PR sandbox lease is busy', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-busy-pr-sandbox-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const busyOwner = spawn(process.execPath, ['-e', 'setInterval(() => {}, 60000)'], { stdio: 'ignore' });
    try {
      const { barePath } = await createBareRemote(wd);
      const busyLockDir = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandbox-locks', 'pr-77');
      await mkdir(busyLockDir, { recursive: true });
      const liveNow = new Date();
      await writeFile(join(busyLockDir, 'lease.json'), JSON.stringify({
        version: 1,
        sandbox_id: 'pr-77',
        owner_pid: busyOwner.pid ?? process.pid,
        owner_run_id: 'busy-run',
        target_url: 'https://github.com/acme/widget/pull/77',
        acquired_at: liveNow.toISOString(),
        heartbeat_at: liveNow.toISOString(),
        expires_at: new Date(liveNow.getTime() + 60_000).toISOString(),
      }, null, 2));

      await assert.rejects(
        () => githubCommand(
          ['start', 'https://github.com/acme/widget/pull/77'],
          {
            env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
            homeDir,
            now: () => now,
            startLeaseHeartbeat: () => {},
            fetchImpl: createFetchStub({
              '/user': { login: 'dkropachev' },
              '/repos/acme/widget': {
                name: 'widget',
                full_name: repoSlug,
                clone_url: barePath,
                default_branch: 'main',
                html_url: 'https://github.com/acme/widget',
              },
              '/repos/acme/widget/issues/77': {
                number: 77,
                title: 'PR 77',
                body: 'PR body',
                html_url: 'https://github.com/acme/widget/pull/77',
                state: 'open',
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
                pull_request: {},
              },
              '/repos/acme/widget/issues/77/comments?per_page=100': [],
              '/repos/acme/widget/pulls/77': {
                number: 77,
                title: 'PR 77',
                body: 'PR body',
                html_url: 'https://github.com/acme/widget/pull/77',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T09:00:00.000Z',
                user: { login: 'requester' },
                head: { ref: 'feature/pr-77', sha: 'abc123', repo: { full_name: repoSlug } },
                base: { ref: 'main', sha: 'def456', repo: { full_name: repoSlug } },
              },
            }),
            launchWithHud: async () => {},
            writeLine: () => {},
          },
        ),
        /Sandbox pr-77 is busy/i,
      );
    } finally {
      busyOwner.kill();
      await rm(wd, { recursive: true, force: true });
    }
  });


  it('syncs PR review feedback for issue-based runs once a PR has been published', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-sync-issue-pr-feedback-'));
    const homeDir = join(wd, 'home');
    const now = new Date('2026-04-03T11:00:00.000Z');
    try {
      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const sandboxPath = join(managedRepoRoot, 'sandboxes', 'issue-42-pr-123456789012');
      const runId = 'gh-test-issue-1';
      await mkdir(join(sandboxPath, '.nana'), { recursive: true });
      await writeFile(join(sandboxPath, '.nana', 'sandbox.json'), JSON.stringify({
        version: 1,
        sandbox_id: 'issue-42-pr-123456789012',
        repo_slug: 'acme/widget',
        repo_name: 'widget',
        sandbox_path: sandboxPath,
        repo_checkout_path: join(sandboxPath, 'repo'),
        branch_name: 'nana/issue-42/issue-42-pr-123456789012',
        base_ref: 'origin/main',
        target_kind: 'issue',
        target_number: 42,
        created_at: '2026-04-03T10:00:00.000Z',
        updated_at: '2026-04-03T10:00:00.000Z',
      }, null, 2));
      await mkdir(join(managedRepoRoot, 'runs', runId), { recursive: true });
      await mkdir(join(homeDir, '.nana', 'github-workon'), { recursive: true });
      await writeFile(join(homeDir, '.nana', 'github-workon', 'latest-run.json'), JSON.stringify({
        repo_root: managedRepoRoot,
        run_id: runId,
      }, null, 2));
      await writeFile(join(managedRepoRoot, 'runs', runId, 'manifest.json'), JSON.stringify({
        version: 3,
        run_id: runId,
        created_at: '2026-04-03T10:00:00.000Z',
        updated_at: '2026-04-03T10:00:00.000Z',
        repo_slug: 'acme/widget',
        repo_owner: 'acme',
        repo_name: 'widget',
        managed_repo_root: managedRepoRoot,
        source_path: join(managedRepoRoot, 'source'),
        sandbox_id: 'issue-42-pr-123456789012',
        sandbox_path: sandboxPath,
        sandbox_repo_path: join(sandboxPath, 'repo'),
        considerations_active: [],
        role_layout: 'split',
        consideration_pipeline: [
          {
            alias: 'coder',
            role: 'executor',
            prompt_roles: ['executor'],
            phase: 'impl',
            mode: 'execute',
            owner: 'self',
            blocking: true,
            purpose: 'Primary implementation lane for feature and bug work.',
          },
        ],
        lane_prompt_artifacts: [],
        team_resolved_aliases: ['coder'],
        team_resolved_roles: ['executor'],
        create_pr_on_complete: false,
        issue_association_number: 42,
        published_pr_number: 91,
        published_pr_url: 'https://github.com/acme/widget/pull/91',
        published_pr_head_ref: 'nana/issue-42/issue-42-pr-123456789012',
        target_kind: 'issue',
        target_number: 42,
        target_title: 'Implement queue healing',
        target_url: 'https://github.com/acme/widget/issues/42',
        target_state: 'open',
        review_reviewer: 'dkropachev',
        api_base_url: 'https://api.github.com',
        default_branch: 'main',
        last_seen_issue_comment_id: 0,
        last_seen_review_id: 0,
        last_seen_review_comment_id: 0,
      }, null, 2));

      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      let appendixPath = '';

      await githubCommand(
        ['sync', '--last'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: 'acme/widget',
              clone_url: 'https://example.invalid/acme/widget.git',
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/42': {
              number: 42,
              title: 'Implement queue healing',
              body: 'Need to keep workers alive after review updates.',
              html_url: 'https://github.com/acme/widget/issues/42',
              state: 'open',
              updated_at: '2026-04-03T09:00:00.000Z',
              user: { login: 'requester' },
            },
            '/repos/acme/widget/issues/42/comments?per_page=100': [],
            '/repos/acme/widget/pulls?state=all&head=acme%3Anana%2Fissue-42%2Fissue-42-pr-123456789012&per_page=100': [
              {
                number: 91,
                title: 'Implement queue healing',
                body: 'PR body',
                html_url: 'https://github.com/acme/widget/pull/91',
                state: 'open',
                merged_at: null,
                updated_at: '2026-04-03T10:20:00.000Z',
                user: { login: 'dkropachev' },
                head: { ref: 'nana/issue-42/issue-42-pr-123456789012', sha: 'abc123', repo: { full_name: 'acme/widget' } },
                base: { ref: 'main', sha: 'def456', repo: { full_name: 'acme/widget' } },
              },
            ],
            '/repos/acme/widget/pulls/91/reviews?per_page=100': [
              {
                id: 5,
                html_url: 'https://github.com/acme/widget/pull/91#pullrequestreview-5',
                body: 'Please tighten the listener API.',
                submitted_at: '2026-04-03T10:30:00.000Z',
                state: 'CHANGES_REQUESTED',
                user: { login: 'dkropachev' },
                commit_id: 'abc123',
              },
            ],
            '/repos/acme/widget/pulls/91/comments?per_page=100': [
              {
                id: 7,
                html_url: 'https://github.com/acme/widget/pull/91#discussion_r7',
                body: 'This callback name is unclear.',
                created_at: '2026-04-03T10:31:00.000Z',
                updated_at: '2026-04-03T10:31:00.000Z',
                path: 'src/main/java/com/scylladb/alternator/keyrouting/KeyRouteAffinityMetricsListener.java',
                line: 12,
                original_line: 12,
                user: { login: 'dkropachev' },
                pull_request_review_id: 5,
              },
            ],
          }),
          launchWithHud: async () => {
            if (!appendixPath) appendixPath = env[GITHUB_APPEND_ENV] || '';
          },
          writeLine: () => {},
        },
      );

      const appendix = await readFile(appendixPath, 'utf-8');
      assert.match(appendix, /Pull request reviews/i);
      assert.match(appendix, /Please tighten the listener API/i);
      assert.match(appendix, /Pull request review comments/i);
      assert.match(appendix, /This callback name is unclear/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('links issue and PR sandboxes via symlinks when a PR references an issue URL', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-link-'));
    const homeDir = join(wd, 'home');
    const repoSlug = 'acme/widget';
    const now = new Date('2026-04-03T10:00:00.000Z');
    const issueSandboxId = `issue-42-pr-${String(now.getTime()).slice(0, 12)}`;
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const { barePath } = await createBareRemote(wd);
      const env = { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv;
      const fetchRoutes = {
        '/user': { login: 'dkropachev' },
        '/repos/acme/widget': {
          name: 'widget',
          full_name: repoSlug,
          clone_url: barePath,
          default_branch: 'main',
          html_url: 'https://github.com/acme/widget',
        },
        '/repos/acme/widget/issues/42': {
          number: 42,
          title: 'Issue 42',
          body: 'Work item',
          html_url: 'https://github.com/acme/widget/issues/42',
          state: 'open',
          updated_at: '2026-04-03T09:00:00.000Z',
          user: { login: 'requester' },
        },
        '/repos/acme/widget/issues/42/comments?per_page=100': [],
        [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [
          {
            number: 77,
            title: 'PR 77',
            body: 'No issue URL needed',
            html_url: 'https://github.com/acme/widget/pull/77',
            state: 'open',
            merged_at: null,
            updated_at: '2026-04-03T09:30:00.000Z',
            user: { login: 'requester' },
            head: { ref: issueBranch, sha: 'abc123', repo: { full_name: repoSlug } },
            base: { ref: 'main', sha: 'def456', repo: { full_name: repoSlug } },
          },
        ],
        '/repos/acme/widget/issues/77': {
          number: 77,
          title: 'PR 77',
          body: 'No issue URL needed',
          html_url: 'https://github.com/acme/widget/pull/77',
          state: 'open',
          updated_at: '2026-04-03T09:30:00.000Z',
          user: { login: 'requester' },
          pull_request: {},
        },
        '/repos/acme/widget/issues/77/comments?per_page=100': [],
        '/repos/acme/widget/pulls/77': {
          number: 77,
          title: 'PR 77',
          body: 'No issue URL needed',
          html_url: 'https://github.com/acme/widget/pull/77',
          state: 'open',
          merged_at: null,
          updated_at: '2026-04-03T09:30:00.000Z',
          user: { login: 'requester' },
          head: { ref: issueBranch, sha: 'abc123', repo: { full_name: repoSlug } },
          base: { ref: 'main', sha: 'def456', repo: { full_name: repoSlug } },
        },
        '/repos/acme/widget/pulls/77/reviews?per_page=100': [],
        '/repos/acme/widget/pulls/77/comments?per_page=100': [],
      };

      await githubCommand(
        ['start', 'https://github.com/acme/widget/issues/42'],
        {
          env,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub(fetchRoutes),
          launchWithHud: async () => {},
          writeLine: () => {},
        },
      );

      await assert.rejects(
        () => githubCommand(
          ['start', 'https://github.com/acme/widget/pull/77'],
          {
            env,
            homeDir,
            now: () => now,
            startLeaseHeartbeat: () => {},
            fetchImpl: createFetchStub(fetchRoutes),
            launchWithHud: async () => {
              throw new Error('stop-before-publication');
            },
            writeLine: () => {},
          },
        ),
        /stop-before-publication/i,
      );

      const prSandboxPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', 'pr-77');
      const issueSandboxPath = join(homeDir, '.nana', 'repos', 'acme', 'widget', 'sandboxes', issueSandboxId);
      assert.equal((await lstat(prSandboxPath)).isSymbolicLink(), true);
      assert.equal(resolve(dirname(prSandboxPath), await readlink(prSandboxPath)), issueSandboxPath);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });

  it('drops a merged PR sandbox and removes reverse links on sync', async () => {
    const wd = await mkdtemp(join(tmpdir(), 'nana-github-merged-pr-'));
    const homeDir = join(wd, 'home');
    const now = new Date('2026-04-03T11:00:00.000Z');
    const issueSandboxId = 'issue-42-pr-123456789012';
    const issueBranch = `nana/issue-42/${issueSandboxId}`;
    try {
      const managedRepoRoot = join(homeDir, '.nana', 'repos', 'acme', 'widget');
      const issueSandboxPath = join(managedRepoRoot, 'sandboxes', issueSandboxId);
      const prSandboxPath = join(managedRepoRoot, 'sandboxes', 'pr-77');
      await mkdir(issueSandboxPath, { recursive: true });
      await symlink(relative(dirname(prSandboxPath), issueSandboxPath), prSandboxPath, 'dir');
      await mkdir(join(homeDir, '.nana', 'github-workon'), { recursive: true });
      await mkdir(join(managedRepoRoot, 'runs', 'gh-test-1'), { recursive: true });
      await writeFile(join(homeDir, '.nana', 'github-workon', 'latest-run.json'), JSON.stringify({
        repo_root: managedRepoRoot,
        run_id: 'gh-test-1',
      }, null, 2));
      await writeFile(join(managedRepoRoot, 'runs', 'gh-test-1', 'manifest.json'), JSON.stringify({
        version: 3,
        run_id: 'gh-test-1',
        created_at: '2026-04-03T10:00:00.000Z',
        updated_at: '2026-04-03T10:00:00.000Z',
        repo_slug: 'acme/widget',
        repo_owner: 'acme',
        repo_name: 'widget',
        managed_repo_root: managedRepoRoot,
        source_path: join(managedRepoRoot, 'source'),
        sandbox_id: 'pr-77',
        sandbox_path: prSandboxPath,
        sandbox_repo_path: join(prSandboxPath, 'repo'),
        considerations_active: [],
        role_layout: 'split',
        consideration_pipeline: [
          {
            alias: 'coder',
            role: 'executor',
            prompt_roles: ['executor'],
            phase: 'impl',
            mode: 'execute',
            owner: 'self',
            blocking: true,
            purpose: 'Primary implementation lane for feature and bug work.',
          },
        ],
        lane_prompt_artifacts: [],
        team_resolved_aliases: ['coder'],
        team_resolved_roles: ['executor'],
        target_kind: 'pr',
        target_number: 77,
        target_title: 'PR 77',
        target_url: 'https://github.com/acme/widget/pull/77',
        target_state: 'open',
        review_reviewer: 'dkropachev',
        api_base_url: 'https://api.github.com',
        default_branch: 'main',
        last_seen_issue_comment_id: 0,
        last_seen_review_id: 0,
        last_seen_review_comment_id: 0,
        pr_head_ref: 'feature/pr-77',
        pr_head_sha: 'abc123',
        pr_head_repo: 'acme/widget',
        pr_base_ref: 'main',
        pr_base_sha: 'def456',
        pr_base_repo: 'acme/widget',
      }, null, 2));

      const messages: string[] = [];
      await githubCommand(
        ['sync', '--last'],
        {
          env: { ...process.env, GH_TOKEN: 'test-token' } as NodeJS.ProcessEnv,
          homeDir,
          now: () => now,
          startLeaseHeartbeat: () => {},
          fetchImpl: createFetchStub({
            '/user': { login: 'dkropachev' },
            '/repos/acme/widget': {
              name: 'widget',
              full_name: 'acme/widget',
              clone_url: '/tmp/unused-remote.git',
              default_branch: 'main',
              html_url: 'https://github.com/acme/widget',
            },
            '/repos/acme/widget/issues/77': {
              number: 77,
              title: 'PR 77',
              body: 'No issue URL needed',
              html_url: 'https://github.com/acme/widget/pull/77',
              state: 'closed',
              updated_at: '2026-04-03T11:00:00.000Z',
              user: { login: 'requester' },
              pull_request: {},
            },
            '/repos/acme/widget/pulls/77': {
              number: 77,
              title: 'PR 77',
              body: 'No issue URL needed',
              html_url: 'https://github.com/acme/widget/pull/77',
              state: 'closed',
              merged_at: '2026-04-03T11:00:00.000Z',
              updated_at: '2026-04-03T11:00:00.000Z',
              user: { login: 'requester' },
              head: { ref: issueBranch, sha: 'abc123', repo: { full_name: 'acme/widget' } },
              base: { ref: 'main', sha: 'def456', repo: { full_name: 'acme/widget' } },
            },
            [`/repos/acme/widget/pulls?state=all&head=${encodeURIComponent(`acme:${issueBranch}`)}&per_page=100`]: [
              {
                number: 77,
                title: 'PR 77',
                body: 'No issue URL needed',
                html_url: 'https://github.com/acme/widget/pull/77',
                state: 'closed',
                merged_at: '2026-04-03T11:00:00.000Z',
                updated_at: '2026-04-03T11:00:00.000Z',
                user: { login: 'requester' },
                head: { ref: issueBranch, sha: 'abc123', repo: { full_name: 'acme/widget' } },
                base: { ref: 'main', sha: 'def456', repo: { full_name: 'acme/widget' } },
              },
            ],
            '/repos/acme/widget/issues/77/comments?per_page=100': [],
            '/repos/acme/widget/pulls/77/reviews?per_page=100': [],
            '/repos/acme/widget/pulls/77/comments?per_page=100': [],
          }),
          launchWithHud: async () => {},
          writeLine: (line) => messages.push(line),
        },
      );

      assert.equal(existsSync(prSandboxPath), false);
      assert.equal(existsSync(issueSandboxPath), true);
      assert.match(messages.join('\n'), /dropped sandbox pr-77/i);
    } finally {
      await rm(wd, { recursive: true, force: true });
    }
  });
});

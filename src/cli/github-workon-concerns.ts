import { existsSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';

export interface GithubLaneConcernDescriptor {
  key: string;
  pathPrefixes?: string[];
  pathContains?: string[];
  fileNames?: string[];
  extensions?: string[];
  buildManifests?: string[];
  docsAndConfig?: boolean;
  matchUnknown?: boolean;
  fallbackTokens?: string[];
}

export type GithubConcernDescriptorSource = 'default' | 'repo_override' | 'persisted';

export interface GithubConcernRegistryDiagnostic {
  path: string;
  code: 'parse_error' | 'unsupported_version' | 'invalid_shape';
  message: string;
}

export interface GithubLaneConcernMatchReason {
  file: string;
  kind: 'direct' | 'fallback' | 'unknown';
  evidence: string;
  rule_source: GithubConcernDescriptorSource;
}

export interface GithubLaneConcernMatchResult {
  concern_key: string;
  descriptor_source: GithubConcernDescriptorSource;
  override_path?: string;
  matched_files: string[];
  direct_files: string[];
  fallback_files: string[];
  unknown_files: string[];
  unmatched_files: string[];
  reasons: GithubLaneConcernMatchReason[];
}

interface GithubLaneConcernOverrideEntry extends Partial<GithubLaneConcernDescriptor> {
  replace?: boolean;
}

interface GithubLaneConcernOverrideFile {
  version?: number;
  lanes?: Record<string, GithubLaneConcernOverrideEntry>;
}

export interface GithubConcernRegistryDetails {
  registry: Record<string, GithubLaneConcernDescriptor>;
  descriptor_sources: Record<string, { source: Exclude<GithubConcernDescriptorSource, 'persisted'>; path?: string }>;
  diagnostics: GithubConcernRegistryDiagnostic[];
}

export const DEFAULT_LANE_CONCERN_DESCRIPTORS: Record<string, GithubLaneConcernDescriptor> = {
  architect: {
    key: 'architecture',
    pathPrefixes: ['src/', 'lib/', 'internal/', 'cmd/', 'app/', 'server/', 'client/', 'docs/'],
    fileNames: ['README.md'],
    fallbackTokens: ['arch', 'architecture', 'module', 'component', 'boundary', 'interface', 'design'],
  },
  'api-reviewer': {
    key: 'api',
    pathPrefixes: ['api/', 'public/', 'schema/', 'schemas/', 'openapi/', 'proto/', 'docs/', 'config/'],
    pathContains: ['/api/', '/schema/', '/schemas/', '/openapi/', '/proto/'],
    fileNames: ['README.md'],
    extensions: ['.md', '.yaml', '.yml', '.json', '.proto', '.toml'],
    docsAndConfig: true,
    fallbackTokens: ['api', 'schema', 'openapi', 'contract', 'endpoint', 'route', 'graphql', 'proto'],
  },
  'security-reviewer': {
    key: 'security',
    pathPrefixes: ['auth/', 'security/', 'secrets/', 'credentials/', 'config/', 'tls/', 'ssl/', 'net/', 'http/', 'logging/'],
    pathContains: ['/auth/', '/security/', '/secret', '/credential', '/token', '/tls/', '/ssl/', '/http/', '/net/', '/logging/'],
    docsAndConfig: false,
    fallbackTokens: ['auth', 'security', 'secret', 'credential', 'token', 'permission', 'rbac', 'tls', 'ssl'],
  },
  'dependency-expert': {
    key: 'dependency',
    fileNames: [
      'package.json', 'pnpm-lock.yaml', 'yarn.lock', 'package-lock.json',
      'Cargo.toml', 'Cargo.lock', 'pom.xml', 'build.gradle', 'settings.gradle',
      'gradle.properties', 'requirements.txt', 'poetry.lock', 'pyproject.toml',
      'go.mod', 'go.sum', 'Makefile', 'Dockerfile', 'docker-compose.yml',
    ],
    buildManifests: [
      'package.json', 'pnpm-lock.yaml', 'yarn.lock', 'package-lock.json',
      'Cargo.toml', 'Cargo.lock', 'pom.xml', 'build.gradle', 'settings.gradle',
      'gradle.properties', 'requirements.txt', 'poetry.lock', 'pyproject.toml',
      'go.mod', 'go.sum', 'Makefile', 'Dockerfile', 'docker-compose.yml',
    ],
    fallbackTokens: ['dependency', 'dependencies', 'package', 'lock', 'vendor', 'build', 'workspace'],
  },
  'perf-reviewer': {
    key: 'performance',
    pathPrefixes: ['perf/', 'performance/', 'cache/', 'router/', 'routing/', 'query/', 'queries/', 'runtime/', 'benchmark/', 'benchmarks/'],
    pathContains: ['/perf/', '/performance/', '/cache/', '/router', '/routing', '/query', '/runtime/', '/benchmark/'],
    fallbackTokens: ['perf', 'performance', 'latency', 'throughput', 'cache', 'benchmark', 'runtime', 'query'],
  },
  'perf-coder': {
    key: 'performance',
    pathPrefixes: ['perf/', 'performance/', 'cache/', 'router/', 'routing/', 'query/', 'queries/', 'runtime/', 'benchmark/', 'benchmarks/'],
    pathContains: ['/perf/', '/performance/', '/cache/', '/router', '/routing', '/query', '/runtime/', '/benchmark/'],
    fallbackTokens: ['perf', 'performance', 'latency', 'throughput', 'cache', 'benchmark', 'runtime', 'query'],
  },
  'test-engineer': {
    key: 'tests',
    pathPrefixes: ['test/', 'tests/', 'spec/', 'specs/', '__tests__/', 'src/'],
    pathContains: ['/test/', '/tests/', '/spec/', '/__tests__/'],
    extensions: ['.test.ts', '.test.js', '.spec.ts', '.spec.js', '.test.tsx', '.spec.tsx'],
    fallbackTokens: ['test', 'tests', 'spec', 'specs', 'fixture', 'fixtures', 'mock', 'mocks'],
  },
  'style-reviewer': {
    key: 'style',
    matchUnknown: true,
    docsAndConfig: true,
    fallbackTokens: ['style', 'lint', 'format', 'naming', 'readme', 'docs', 'config'],
  },
};

const CONCERN_OVERRIDE_FILE_CANDIDATES = [
  ['.nana', 'work-on-concerns.json'],
  ['.github', 'nana-work-on-concerns.json'],
] as const;

function uniqueStrings(values: readonly string[] | undefined): string[] | undefined {
  if (!values || values.length === 0) return undefined;
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))];
}

function normalizeDescriptor(descriptor: GithubLaneConcernDescriptor): GithubLaneConcernDescriptor {
  return {
    ...descriptor,
    key: descriptor.key.trim(),
    pathPrefixes: uniqueStrings(descriptor.pathPrefixes),
    pathContains: uniqueStrings(descriptor.pathContains),
    fileNames: uniqueStrings(descriptor.fileNames),
    extensions: uniqueStrings(descriptor.extensions),
    buildManifests: uniqueStrings(descriptor.buildManifests),
    fallbackTokens: uniqueStrings(descriptor.fallbackTokens?.map((token) => token.toLowerCase())),
  };
}

function mergeDescriptor(
  base: GithubLaneConcernDescriptor,
  override: GithubLaneConcernOverrideEntry,
): GithubLaneConcernDescriptor {
  const replacement: GithubLaneConcernDescriptor = normalizeDescriptor({
    key: override.key ?? base.key,
    pathPrefixes: override.pathPrefixes,
    pathContains: override.pathContains,
    fileNames: override.fileNames,
    extensions: override.extensions,
    buildManifests: override.buildManifests,
    docsAndConfig: override.docsAndConfig,
    matchUnknown: override.matchUnknown,
    fallbackTokens: override.fallbackTokens,
  });
  if (override.replace) return replacement;
  return normalizeDescriptor({
    key: override.key ?? base.key,
    pathPrefixes: [...(base.pathPrefixes ?? []), ...(override.pathPrefixes ?? [])],
    pathContains: [...(base.pathContains ?? []), ...(override.pathContains ?? [])],
    fileNames: [...(base.fileNames ?? []), ...(override.fileNames ?? [])],
    extensions: [...(base.extensions ?? []), ...(override.extensions ?? [])],
    buildManifests: [...(base.buildManifests ?? []), ...(override.buildManifests ?? [])],
    docsAndConfig: override.docsAndConfig ?? base.docsAndConfig,
    matchUnknown: override.matchUnknown ?? base.matchUnknown,
    fallbackTokens: [...(base.fallbackTokens ?? []), ...(override.fallbackTokens ?? [])],
  });
}

function normalizedPathTokens(filePath: string): string[] {
  return filePath
    .replace(/\\/g, '/')
    .toLowerCase()
    .split(/[^a-z0-9]+/)
    .map((token) => token.trim())
    .filter(Boolean);
}

function directMatchEvidence(descriptor: GithubLaneConcernDescriptor, filePath: string): string | undefined {
  const normalized = filePath.replace(/\\/g, '/');
  if (descriptor.key === 'style') return 'style catch-all lane';
  if (descriptor.fileNames?.some((name) => normalized.endsWith(name))) return 'file name match';
  if (descriptor.buildManifests?.some((name) => normalized.endsWith(name))) return 'build manifest match';
  if (descriptor.pathPrefixes?.some((prefix) => normalized.startsWith(prefix))) return 'path prefix match';
  if (descriptor.pathContains?.some((piece) => normalized.includes(piece))) return 'path fragment match';
  if (descriptor.extensions?.some((extension) => normalized.endsWith(extension))) return 'extension match';
  if (descriptor.docsAndConfig && /(^|\/)(docs?|config)(\/|$)|README\.md$/i.test(normalized)) return 'docs/config heuristic';
  return undefined;
}

function fallbackMatchEvidence(descriptor: GithubLaneConcernDescriptor, filePath: string): string | undefined {
  const tokens = normalizedPathTokens(filePath);
  if (tokens.length === 0) return undefined;
  const matched = (descriptor.fallbackTokens ?? []).filter((token) => tokens.includes(token));
  return matched.length > 0 ? `fallback token match: ${matched.join(', ')}` : undefined;
}

export function descriptorMatchesFileDirect(
  descriptor: GithubLaneConcernDescriptor,
  filePath: string,
): boolean {
  return Boolean(directMatchEvidence(descriptor, filePath));
}

export function resolveLaneConcernDescriptor(
  laneAlias: string,
  registry: Readonly<Record<string, GithubLaneConcernDescriptor>>,
  persistedDescriptor?: GithubLaneConcernDescriptor,
): GithubLaneConcernDescriptor {
  return normalizeDescriptor(persistedDescriptor ?? registry[laneAlias] ?? { key: laneAlias });
}

export async function resolveGithubConcernRegistryDetails(
  repoCheckoutPath: string,
): Promise<GithubConcernRegistryDetails> {
  const registry: Record<string, GithubLaneConcernDescriptor> = {};
  const descriptorSources: Record<string, { source: Exclude<GithubConcernDescriptorSource, 'persisted'>; path?: string }> = {};
  for (const [alias, descriptor] of Object.entries(DEFAULT_LANE_CONCERN_DESCRIPTORS)) {
    registry[alias] = normalizeDescriptor(descriptor);
    descriptorSources[alias] = { source: 'default' };
  }

  const diagnostics: GithubConcernRegistryDiagnostic[] = [];
  for (const candidate of [...CONCERN_OVERRIDE_FILE_CANDIDATES].reverse()) {
    const path = join(repoCheckoutPath, ...candidate);
    const overrideSource: Exclude<GithubConcernDescriptorSource, 'persisted'> = 'repo_override';
    if (!existsSync(path)) continue;
    try {
      const parsed = JSON.parse(await readFile(path, 'utf-8')) as GithubLaneConcernOverrideFile;
      if (parsed.version != null && parsed.version !== 1) {
        diagnostics.push({
          path,
          code: 'unsupported_version',
          message: `Unsupported concern override version ${parsed.version}; expected 1.`,
        });
        continue;
      }
      if (!parsed || typeof parsed !== 'object' || !parsed.lanes || typeof parsed.lanes !== 'object') {
        diagnostics.push({
          path,
          code: 'invalid_shape',
          message: 'Concern override file must contain a top-level "lanes" object.',
        });
        continue;
      }
      for (const [alias, override] of Object.entries(parsed.lanes ?? {})) {
        const base = DEFAULT_LANE_CONCERN_DESCRIPTORS[alias] ?? { key: override.key ?? alias };
        registry[alias] = mergeDescriptor(base, override);
        descriptorSources[alias] = { source: overrideSource, path };
      }
    } catch {
      diagnostics.push({
        path,
        code: 'parse_error',
        message: 'Concern override file is not valid JSON.',
      });
    }
  }
  return {
    registry,
    descriptor_sources: descriptorSources,
    diagnostics,
  };
}

export async function resolveGithubConcernRegistry(
  repoCheckoutPath: string,
): Promise<Record<string, GithubLaneConcernDescriptor>> {
  return (await resolveGithubConcernRegistryDetails(repoCheckoutPath)).registry;
}

export function fileMatchesAnyKnownConcern(
  filePath: string,
  registry: Readonly<Record<string, GithubLaneConcernDescriptor>>,
): boolean {
  return Object.values(registry).some((descriptor) =>
    descriptor.key !== 'style' && (Boolean(directMatchEvidence(descriptor, filePath)) || Boolean(fallbackMatchEvidence(descriptor, filePath))));
}

export function resolveConcernMatchForFiles(input: {
  descriptor: GithubLaneConcernDescriptor;
  registry: Readonly<Record<string, GithubLaneConcernDescriptor>>;
  descriptorSource?: GithubConcernDescriptorSource;
  overridePath?: string;
  changedFiles: string[];
  sourceByAlias?: Readonly<Record<string, { source: Exclude<GithubConcernDescriptorSource, 'persisted'>; path?: string }>>;
}): GithubLaneConcernMatchResult {
  const matchedFiles = new Set<string>();
  const directFiles = new Set<string>();
  const fallbackFiles = new Set<string>();
  const unknownFiles = new Set<string>();
  const unmatchedFiles = new Set<string>();
  const reasons: GithubLaneConcernMatchReason[] = [];
  const resolvedSource = input.descriptorSource ?? 'default';
  const ruleSource: GithubConcernDescriptorSource = resolvedSource;

  for (const file of input.changedFiles) {
    const directEvidence = directMatchEvidence(input.descriptor, file);
    if (directEvidence) {
      matchedFiles.add(file);
      directFiles.add(file);
      reasons.push({ file, kind: 'direct', evidence: directEvidence, rule_source: ruleSource });
      continue;
    }
    const fallbackEvidence = fallbackMatchEvidence(input.descriptor, file);
    if (fallbackEvidence) {
      matchedFiles.add(file);
      fallbackFiles.add(file);
      reasons.push({ file, kind: 'fallback', evidence: fallbackEvidence, rule_source: ruleSource });
      continue;
    }
    if (input.descriptor.matchUnknown && !fileMatchesAnyKnownConcern(file, input.registry)) {
      matchedFiles.add(file);
      unknownFiles.add(file);
      reasons.push({ file, kind: 'unknown', evidence: 'unknown-file opt-in', rule_source: ruleSource });
      continue;
    }
    unmatchedFiles.add(file);
  }

  return {
    concern_key: input.descriptor.key,
    descriptor_source: resolvedSource,
    override_path: input.overridePath,
    matched_files: [...matchedFiles].sort(),
    direct_files: [...directFiles].sort(),
    fallback_files: [...fallbackFiles].sort(),
    unknown_files: [...unknownFiles].sort(),
    unmatched_files: [...unmatchedFiles].sort(),
    reasons,
  };
}

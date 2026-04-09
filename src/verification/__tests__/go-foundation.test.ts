import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

describe("Go migration foundation", () => {
  it("defines a root Go module and opt-in build scripts", () => {
    const goModPath = join(process.cwd(), "go.mod");
    const packageJsonPath = join(process.cwd(), "package.json");
    assert.equal(existsSync(goModPath), true, `missing go.mod: ${goModPath}`);
    const goMod = readFileSync(goModPath, "utf-8");
    const pkg = JSON.parse(readFileSync(packageJsonPath, "utf-8")) as {
      scripts?: Record<string, string>;
    };

    assert.match(goMod, /^module github\.com\/Yeachan-Heo\/nana/m);
    assert.match(goMod, /^go 1\.24\.0$/m);
    assert.equal(pkg.scripts?.["build:go"], "node scripts/build-go-cli.mjs");
    assert.equal(pkg.scripts?.["test:go"], "go test ./...");
  });

  it("builds the top-level Go shim plus native Go seam shims under bin/go", () => {
    const buildScriptPath = join(process.cwd(), "scripts", "build-go-cli.mjs");
    assert.equal(existsSync(buildScriptPath), true, `missing build script: ${buildScriptPath}`);
    const buildScript = readFileSync(buildScriptPath, "utf-8");

    assert.match(buildScript, /\.\/cmd\/nana/);
    assert.match(buildScript, /\.\/cmd\/nana-runtime/);
    assert.match(buildScript, /\.\/cmd\/nana-explore/);
    assert.match(buildScript, /\.\/cmd\/nana-sparkshell/);
    assert.match(buildScript, /bin", "go", `nana-runtime/);
    assert.match(buildScript, /bin", "go", `nana-explore-harness/);
    assert.match(buildScript, /bin", "go", `nana-sparkshell/);
  });

  it("adds a Go smoke lane to CI without replacing the existing build graph", () => {
    const workflowPath = join(process.cwd(), ".github", "workflows", "ci.yml");
    assert.equal(existsSync(workflowPath), true, `missing workflow: ${workflowPath}`);
    const workflow = readFileSync(workflowPath, "utf-8");

    assert.match(workflow, /go-smoke:/);
    assert.match(workflow, /actions\/setup-go@v5/);
    assert.match(workflow, /go-version-file: go\.mod/);
    assert.match(workflow, /Run Go test suite/);
    assert.match(workflow, /go test \.\/\.\.\./);
    assert.match(workflow, /needs:\s*\[rustfmt, clippy, lint, typecheck, go-smoke, test\]/);
    assert.match(workflow, /needs:\s*\[rustfmt, clippy, lint, typecheck, go-smoke, test, coverage-ts-full, coverage-rust, build\]/);
  });
});

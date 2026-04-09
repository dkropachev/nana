import { mkdirSync, readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");
const pkg = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
const exe = process.platform === "win32" ? ".exe" : "";
const targets = [
  { pkg: "./cmd/nana", out: join(root, "bin", `nana${exe}`) },
  { pkg: "./cmd/nana-runtime", out: join(root, "bin", "go", `nana-runtime${exe}`) },
  { pkg: "./cmd/nana-explore", out: join(root, "bin", "go", `nana-explore-harness${exe}`) },
  { pkg: "./cmd/nana-sparkshell", out: join(root, "bin", "go", `nana-sparkshell${exe}`) },
];

for (const target of targets) {
  mkdirSync(dirname(target.out), { recursive: true });
  const result = spawnSync(
    "go",
    [
      "build",
      "-ldflags",
      `-X github.com/Yeachan-Heo/nana/internal/version.Version=${pkg.version}`,
      "-o",
      target.out,
      target.pkg,
    ],
    {
      cwd: root,
      stdio: "inherit",
    },
  );

  if (result.error) {
    console.error(`[build-go-cli] failed to start go build for ${target.pkg}: ${result.error.message}`);
    process.exit(1);
  }

  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

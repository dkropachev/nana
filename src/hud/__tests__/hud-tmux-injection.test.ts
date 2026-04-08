/**
 * Tests for shell command injection hardening in the tmux HUD launcher.
 *
 * The first describe block reproduces the vulnerability that existed when
 * launchTmuxPane() built a command string via template-literal interpolation
 * and passed it to execSync().  The second block verifies the hardened
 * buildTmuxSplitArgs() + shellEscape() approach is safe.
 */

import { describe, it } from 'node:test';
import assert from 'node:assert/strict';
import { shellEscape, buildTmuxSplitArgs } from '../index.js';
import { HUD_TMUX_HEIGHT_LINES } from '../constants.js';

// ── Vulnerability demonstration (old code) ──────────────────────────────────

describe('VULNERABILITY – old string-interpolation approach', () => {
  /**
   * Reproduces the exact string construction from the original code:
   *   const cmd  = `node ${nanaBin} hud --watch${presetArg}`;
   *   execSync(`tmux split-window -v -l 4 -c "${cwd}" '${cmd}'`);
   */
  function buildOldCommand(cwd: string, nanaBin: string, preset?: string): string {
    const presetArg = preset ? ` --preset=${preset}` : '';
    const cmd = `node ${nanaBin} hud --watch${presetArg}`;
    return `tmux split-window -v -l ${HUD_TMUX_HEIGHT_LINES} -c "${cwd}" '${cmd}'`;
  }

  it('cwd containing $() is injectable via double-quote context', () => {
    const maliciousCwd = '/tmp/foo"$(touch /tmp/pwned)"bar';
    const shellCmd = buildOldCommand(maliciousCwd, '/usr/bin/nana.js');

    // The double-quoted -c argument lets the shell evaluate $()
    assert.ok(
      shellCmd.includes('-c "/tmp/foo"$(touch /tmp/pwned)"bar"'),
      `Old code passes command substitution unescaped: ${shellCmd}`,
    );
  });

  it('cwd containing backticks is injectable via double-quote context', () => {
    const maliciousCwd = '/tmp/`id>/tmp/leak`';
    const shellCmd = buildOldCommand(maliciousCwd, '/usr/bin/nana.js');

    assert.ok(
      shellCmd.includes('`id>/tmp/leak`'),
      `Old code passes backtick command unescaped: ${shellCmd}`,
    );
  });

  it("nanaBin containing single quote breaks out of the '-quoted command", () => {
    const maliciousNana = "/tmp/it';touch /tmp/pwned;echo '/nana.js";
    const shellCmd = buildOldCommand('/home/user', maliciousNana);

    // The injected single quote terminates the tmux shell-command argument
    // early, allowing arbitrary commands to follow.
    assert.ok(
      shellCmd.includes("'node /tmp/it';touch /tmp/pwned;echo '/nana.js"),
      `Old code allows single-quote breakout: ${shellCmd}`,
    );
  });
});

// ── Hardened implementation tests ───────────────────────────────────────────

describe('shellEscape', () => {
  it('wraps a plain string in single quotes', () => {
    assert.equal(shellEscape('/usr/bin/node'), "'/usr/bin/node'");
  });

  it('escapes embedded single quotes', () => {
    assert.equal(shellEscape("it's"), "'it'\\''s'");
  });

  it('handles multiple single quotes', () => {
    assert.equal(shellEscape("a'b'c"), "'a'\\''b'\\''c'");
  });

  it('passes through $() literally inside single quotes', () => {
    const escaped = shellEscape('$(whoami)');
    assert.equal(escaped, "'$(whoami)'");
  });

  it('passes through backticks literally inside single quotes', () => {
    const escaped = shellEscape('`id`');
    assert.equal(escaped, "'`id`'");
  });

  it('handles empty string', () => {
    assert.equal(shellEscape(''), "''");
  });
});

describe('buildTmuxSplitArgs – shell injection hardening', () => {
  it('produces correct argv for normal inputs', () => {
    const args = buildTmuxSplitArgs('/home/user/project', '/usr/local/bin/nana.js');
    assert.deepEqual(args, [
      'split-window', '-v', '-l', String(HUD_TMUX_HEIGHT_LINES),
      // split height should come from shared HUD constants
      '-c', '/home/user/project',
      "node '/usr/local/bin/nana.js' hud --watch",
    ]);
  });

  it('cwd is an isolated array element – never shell-interpreted', () => {
    const maliciousCwd = '/tmp/foo"$(touch /tmp/pwned)"bar';
    const args = buildTmuxSplitArgs(maliciousCwd, '/usr/bin/nana.js');

    // cwd is element [5], passed directly to execFileSync as a tmux arg.
    // tmux receives it as a literal string – no shell expansion.
    assert.equal(args[5], maliciousCwd);

    // The shell command (element [6]) does NOT contain cwd at all.
    assert.ok(!args[6].includes(maliciousCwd));
  });

  it('cwd with backticks is isolated from the shell command', () => {
    const maliciousCwd = '/tmp/`id>/tmp/leak`';
    const args = buildTmuxSplitArgs(maliciousCwd, '/usr/bin/nana.js');
    assert.equal(args[5], maliciousCwd);
    assert.ok(!args[6].includes('`id'));
  });

  it("nanaBin with single quote is properly escaped in command string", () => {
    const maliciousNana = "/tmp/it's/nana.js";
    const args = buildTmuxSplitArgs('/home/user', maliciousNana);
    const cmd = args[6];

    // The single quote must be escaped, not a raw breakout.
    assert.ok(
      cmd.includes("'\\''"),
      `Expected escaped single quote in: ${cmd}`,
    );
    assert.equal(cmd, "node '/tmp/it'\\''s/nana.js' hud --watch");
  });

  it('nanaBin with $() is neutralised by single-quote wrapping', () => {
    const maliciousNana = '/tmp/$(id)/nana.js';
    const args = buildTmuxSplitArgs('/home/user', maliciousNana);
    const cmd = args[6];

    // Inside single quotes, $() is literal.
    assert.equal(cmd, "node '/tmp/$(id)/nana.js' hud --watch");
  });

  it('nanaBin with backticks is neutralised by single-quote wrapping', () => {
    const maliciousNana = '/tmp/`whoami`/nana.js';
    const args = buildTmuxSplitArgs('/home/user', maliciousNana);
    const cmd = args[6];

    assert.equal(cmd, "node '/tmp/`whoami`/nana.js' hud --watch");
  });

  it("nanaBin with ';command' breakout attempt is neutralised", () => {
    const maliciousNana = "/tmp/x';touch /tmp/pwned;echo '/nana.js";
    const args = buildTmuxSplitArgs('/home/user', maliciousNana);
    const cmd = args[6];

    // The shell-escape wraps the entire path in single quotes with internal
    // quotes escaped as '\''.  In a POSIX shell the result is a single word;
    // the semicolons never act as command separators.
    //
    // Raw expected value: node '/tmp/x'\'';touch /tmp/pwned;echo '\''/nana.js' hud --watch
    assert.equal(
      cmd,
      "node '/tmp/x'\\'';touch /tmp/pwned;echo '\\''/nana.js' hud --watch",
    );

    // Both original single quotes are escaped (two '\'' sequences).
    // Each '\'' is: end-quote, backslash-escaped-quote, start-quote
    const escapeCount = (cmd.match(/'\\''/g) || []).length;
    assert.equal(escapeCount, 2, `Expected 2 escape sequences, got ${escapeCount}`);
  });

  it('preset is appended safely', () => {
    const args = buildTmuxSplitArgs('/home/user', '/usr/bin/nana.js', 'minimal');
    const cmd = args[6];
    assert.ok(cmd.endsWith('--preset=minimal'));
  });

  it('absent preset produces no --preset flag', () => {
    const args = buildTmuxSplitArgs('/home/user', '/usr/bin/nana.js');
    const cmd = args[6];
    assert.ok(!cmd.includes('--preset'));
  });

  it('invalid preset is dropped (defense in depth)', () => {
    const args = buildTmuxSplitArgs(
      '/home/user',
      '/usr/bin/nana.js',
      'minimal;touch /tmp/pwned',
    );
    const cmd = args[6];
    assert.equal(cmd, "node '/usr/bin/nana.js' hud --watch");
    assert.ok(!cmd.includes('--preset='));
  });
});

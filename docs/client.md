# rexctl

`rexctl` is a command-line client for Rex-compatible servers. It starts
commands in terminal threads, sends input, reads the screen, and waits for a
thread to finish. It is meant for scripts and bots. `rexctl attach` gives a
person an interactive view of a thread.

It uses only methods that the official Rex server and Tuxosaurus both
advertise. It has been tested against Tuxosaurus only. See
[Mac and Linux differences](#mac-and-linux-differences).

## Build

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/rexctl-linux-amd64 ./cmd/rexctl
```

The binary is static and does not need libghostty. Set `GOOS` and `GOARCH`
for other platforms.

## Terms

- A session holds windows. `rexctl` identifies a session by label or ID.
- A thread is one terminal block in a session, usually one Claude or Codex
  task. `rexctl` identifies a thread by block label or ID.
- Wherever an ID is accepted, a unique prefix also works. On Tuxosaurus the
  prefix may leave out `block:` or `session:`.
- An exact ID match comes first, then an exact label, then an ID prefix. If
  two threads in different sessions share a label, pass `--session`.

## Choosing a server

`rexctl` uses the first of these that is set:

1. `--server URL|ALIAS`
2. `REX_SERVER`
3. `default` in `~/.config/rexctl/config.json` (or `$XDG_CONFIG_HOME/rexctl/config.json`,
   or the path in `REXCTL_CONFIG`)
4. `http://127.0.0.1:51433`, the loopback listener in the Tuxosaurus service

Each of the first three can be a URL or an alias defined in the config file:

```json
{
  "default": "tux",
  "servers": {
    "mac": "https://my-mac-rex.example.ts.net",
    "tux": "unix:///home/me/.local/state/tuxosaurus/server.sock"
  }
}
```

Accepted URL forms are `https://host`, `http://127.0.0.1:PORT` and
`unix:///path/to/socket`. A bare host name is treated as `https://`.

## Global flags

Global flags can go before or after the command name.

| Flag | Meaning |
| --- | --- |
| `--server URL\|ALIAS` | Server to use. |
| `--json` | Print JSON instead of text. |
| `--timeout DUR` | Overall limit, as `30s`, `5m` or plain seconds. The default is 30s. `wait`, `read --follow` and `attach` have no limit unless you set one. |

Flags and arguments can come in any order. Put `--` before anything that
starts with `-` but is not a flag.

## Commands

### start

```sh
rexctl start [--session S] [--label L] [--cwd DIR | --worktree PATH] [--slug NAME] \
  [--close-on-exit] [--no-mise] [--dry-run] [-- CMD...]
```

`start` creates a thread running `CMD`, or the server's default shell if no
command is given. It prints `session_id=...` and `block_id=...`, or a JSON
object with `--json`.

- The session is `--session`, then `--slug`, then `rexctl`. If no session has
  that label or ID, `start` creates one. The new session contains only the new
  thread.
- In an existing session, the thread opens in a new window and does not take
  focus.
- The thread label is `--label`, then `--slug`, then the command's base name.
- `--cwd` is the working directory on the server host. `--worktree PATH` is
  the same as `--cwd PATH`. Relative paths are resolved against `rexctl`'s
  current directory, which is only right when the client and server share a
  host.
- On Tuxosaurus the command runs through the server's login shell, so the
  user's shell profile applies, including mise if the profile activates it.
- By default the thread is kept after its command exits, so `read` and
  `status` still work and the exit code stays available. `--close-on-exit`
  lets the server remove it instead.
- `--no-mise` is accepted so existing scripts keep working. It has no effect.
- `--dry-run` connects, lists sessions, prints the requests `start` would
  send, and exits without changing anything.

### send

```sh
rexctl send [--enter] [--enter-delay 150ms] THREAD TEXT...
rexctl send [--enter] THREAD - < prompt.txt
```

`send` writes text to the thread's terminal. Multiple `TEXT` arguments are
joined with spaces. `-` reads the text from standard input.

No Enter key is sent by default. `--enter` sends Enter after the text, as a
separate write after `--enter-delay`. Some full-screen programs, including
Claude Code, treat text and Enter that arrive together as a paste and insert a
newline instead of submitting. To embed a newline as typed input, use `key`.

### key

```sh
rexctl key THREAD KEY...
```

`key` sends named keys in tmux style. Names are case-insensitive:

| Names | Sends |
| --- | --- |
| `Enter`, `Tab`, `BTab`, `Esc`, `Space`, `BSpace`/`Backspace` | The usual bytes. |
| `Up`, `Down`, `Left`, `Right`, `Home`, `End` | Normal-mode cursor sequences (`ESC [ A`). |
| `PageUp`/`PPage`, `PageDown`/`NPage`, `Insert`/`IC`, `Delete`/`DC`, `F1` to `F12` | xterm sequences. |
| `C-a` to `C-z`, `C-@`, `C-[`, `C-\`, `C-]`, `C-^`, `C-_`, `C-?`, `C-Space` | Control characters, for example `C-c`, `C-d`, `C-z`, `C-l`. |
| `M-KEY` | Escape followed by `KEY`. |
| any single character | That character. |

### read

```sh
rexctl read [--lines N] [--vt] [--no-trim] [--follow] THREAD
```

`read` prints the screen and scrollback as plain text, using the terminal
`format` method. `--lines N` keeps the last N lines. `--vt` keeps colors and
other escape sequences.

`--follow` prints the screen, then streams new output until you press Ctrl-C,
the timeout passes, or the process exits. The stream is plain text with escape
sequences removed. With `--vt` it is the raw terminal output. Cursor movement
is not emulated, so programs that redraw the screen produce untidy text; use
plain `read` for an exact screen. A few lines around the start may print
twice.

### status

```sh
rexctl status [--session S] [THREAD|SESSION]
```

`status` prints one row per thread. With a thread it prints that thread (one
JSON object with `--json`). With a session, or with no argument, it prints
every thread in it, or in all sessions (a JSON array).

Fields: `state`, `reason`, `exit_code` (if the process exited), `title`,
`pwd`, `child`, `foreground` and `program` (the newest program status record).

`state` is decided in this order:

1. `exited`: the thread's process has exited. `exit_code` is set.
2. The newest program status record. Programs report it through terminal
   escape sequences. Tuxosaurus also infers it for known tools such as
   Claude Code and Codex. `working` becomes `running`. `blocked` and `idle`
   become `waiting`. `done` becomes `done`. `error` becomes `error`.
3. `waiting` if a shell is the foreground process (the thread is at a
   prompt).
4. `running` otherwise.

These are heuristics. A program that never reports status and does not exit
will show as `running` even when it is waiting for input. Use
`wait --for idle` or `wait --pattern` in that case.

### wait

```sh
rexctl wait [--for exit|done|idle|pattern] [--pattern RE] [--idle SECS] \
  [--timeout DUR] [--exit-status] THREAD
```

`wait` blocks until the condition holds, then prints the final status. It
exits 0 when the condition is met and 124 on timeout.

| Condition | Met when |
| --- | --- |
| `done` (default) | `state` is `done`, `error` or `exited`, or the thread was removed. |
| `exit` | The process exited, or the thread was removed. |
| `idle` | No output for `--idle` seconds (default 5), or the process exited. |
| `pattern` | The Go regular expression `--pattern` matches the plain screen text. `^` and `$` match at the start and end of each line. `--pattern` alone implies this condition. |

`wait` learns of a process exit from the server's `child_exited` event, the
exit marker on the data stream, and by polling the process method every
second. If the thread was removed and none of these reported an exit code,
`exit_code` is absent.

`--exit-status` makes `rexctl` exit with the thread's exit code, when known.

### stop

```sh
rexctl stop [--signal int] [--grace 5s] THREAD
```

`stop` closes the thread. The server hangs up the thread's process group.
With `--signal int`, `stop` first sends Ctrl-C and waits up to `--grace` for
the process to exit, then closes the thread.

### ls

```sh
rexctl ls            # sessions, with thread counts
rexctl ls SESSION    # threads in a session, with state
```

### close

```sh
rexctl close SESSION
```

`close` destroys the session and every thread in it.

### attach

```sh
rexctl attach [--detach-key KEY] [--resize take|advisory|off] THREAD
```

`attach` puts your terminal in raw mode, repaints the thread's screen, then
shows live output and forwards your keystrokes.

- Press Ctrl-\ to detach. `--detach-key` takes any single-byte key name, such
  as `C-]`. The thread keeps running.
- If the process exits, `attach` returns.
- `--resize take` (default) resizes the thread to your terminal and follows
  window size changes. `advisory` records your size but resizes only if no
  other client owns the size. `off` never resizes. On detach, `rexctl`
  releases its size so another client's size can apply again.
- Each keystroke is one control request, so typing over a slow link lags.

## Examples

Run a one-shot Claude task in a worktree and collect the result:

```sh
rexctl start --slug fix-login --worktree ~/src/app-fix-login -- \
  claude -p "Fix the login redirect bug and run the tests"
rexctl wait fix-login --for exit --timeout 30m
rexctl read fix-login --lines 80
rexctl stop fix-login
```

The same with Codex, using JSON for a bot:

```sh
rexctl --json start --session tasks --label lint-pass --cwd ~/src/app -- \
  codex exec "Fix all lint errors"
rexctl --json wait lint-pass --for exit --exit-status --timeout 1h
rexctl --json read lint-pass
```

An interactive Claude session that a bot prompts and a person can join:

```sh
rexctl start --session tasks --label review --cwd ~/src/app -- claude
rexctl wait review --for idle --idle 3
rexctl send review --enter "Review the diff on this branch"
rexctl wait review --for done --timeout 20m
rexctl attach review            # Ctrl-\ to leave it running
```

## Exit status

| Code | Meaning |
| --- | --- |
| 0 | Success. |
| 1 | Error, including connection failures and server errors. |
| 2 | Usage error, including an ambiguous name. |
| 3 | Session or thread not found. |
| 124 | Timeout. |

## Mac and Linux differences

Tuxosaurus implements the Rex API, but `rexctl` has only been tested against
Tuxosaurus. These points are unverified on the official Mac server:

- Shell options. The Mac server's published options schema for
  `com.superlogical.terminal.shell` lists only `cwd`. Tuxosaurus also accepts
  `command` and `exit.on_completion`, and `start` sends both. The Mac server
  may reject them, or ignore them and run the default shell instead of `CMD`.
  `--close-on-exit` stops `start` from sending `exit`.
- Data tickets. Tuxosaurus returns `data.connect` tickets as base64url
  strings, while the published schema describes an array of 16 bytes.
  `rexctl` accepts both.
- Resize modes. `attach` sends `mode` with `terminal.resize` and retries
  without it if the server rejects it.
- Methods only the Mac server provides (`server.apple.*`, `server.stop`,
  `session.move`, `session.own_block`, `session.disown_block`) are not used.
- `rexctl` lists only terminal blocks, so blocks of other types, such as the
  Mac server's onboarding block, are not threads.
- Snapshot skipping follows the record framing in Ghostty's
  `src/terminal/snapshot/record.zig`: version 1, ending with a FINISH record
  (tag 6). A server that sends another snapshot version may need a decoder
  change.

## Tests

The unit tests need no server and pass with `CGO_ENABLED=0`:

```sh
CGO_ENABLED=0 go test ./internal/rexclient/... ./cmd/rexctl
```

The integration tests start an isolated Tuxosaurus on a temporary Unix socket
and skip unless a server binary is given:

```sh
bash scripts/build.sh native
go test ./internal/rexclient ./cmd/rexctl -count=1 -args \
  -server-binary "$PWD/bin/tuxosaurus-$(go env GOOS)-$(go env GOARCH)"
```

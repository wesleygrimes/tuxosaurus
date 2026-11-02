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

- `BOT` is a session: one bot's workspace. `rexctl` finds it by label or ID.
- `RUN` is a thread: one terminal block in that session, usually one Claude
  or Codex task. `rexctl` finds it by block label or ID. `start` gives each
  run its own window.
- Every thread command takes `BOT RUN` as its first two arguments.
- Wherever an ID is accepted, a unique prefix also works. On Tuxosaurus the
  prefix may leave out `block:` or `session:`.
- An exact ID match comes first, then an exact label, then an ID prefix.

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

| Command | Does |
| --- | --- |
| `start BOT RUN CWD [--label L] [--worktree wes/BRANCH [--slug S]] [--no-mise] [--dry-run] -- CMD...` | Start a run. |
| `send BOT RUN TEXT [--enter]` | Type text. |
| `key BOT RUN KEY...` | Press keys. |
| `read BOT RUN [--tail N]` | Print the screen. |
| `status BOT RUN` | Print one JSON line. |
| `wait BOT RUN [--for 30m]` | Wait for the command to exit. |
| `stop BOT RUN` | Close the run's window. |
| `ls [BOT]` | List sessions, or the runs in one. |
| `close BOT` | End the session. |
| `attach BOT RUN` | Watch and type into a run. |

### start

```sh
rexctl start BOT RUN CWD [--label L] [--worktree wes/BRANCH [--slug S]] \
  [--no-mise] [--dry-run] [--close-on-exit] [-- CMD...]
```

`start` opens a new window in session `BOT` with one thread labelled `RUN`,
running `CMD` in `CWD`. Without `CMD` it runs the server's default shell. It
prints `session_id=...` and `block_id=...`, or a JSON object with `--json`.

- If no session matches `BOT`, `start` creates one. In an existing session
  the new window does not take focus.
- If `BOT` already has a run labelled `RUN`, `start` fails with exit 1. Stop
  the old run first.
- `CWD` is a directory on the server host. Neither server expands `~`, so
  `rexctl` handles it:
  - On a local server (a unix socket or a loopback address), `~` and
    relative paths are resolved on this host.
  - On a remote server, `CWD` must be absolute, or `~` for the server's home
    directory. Anything else is a usage error, because the Mac server quietly
    falls back to the home directory when it cannot use a path.
  - Your shell expands an unquoted `~` on this host before `rexctl` sees
    it. Quote it (`'~'`) or give the server's path, such as
    `/Users/wes/src/app`, when the server is another machine.
- `--label L` sets the window label. The default is `RUN`. The thread label
  is always `RUN`.
- `--worktree BRANCH` runs `CMD` in a git worktree of the repository at
  `CWD`. The worktree is the sibling directory `CWD-SLUG`. `SLUG` is
  `--slug`, or else the branch with a leading `wes/` removed and `/` turned
  into `-`, so `--worktree wes/fix/login` in `~/src/app` uses
  `~/src/app-fix-login`. If the directory does not exist, the server host runs
  `git worktree add`, which checks out `BRANCH` if it exists and otherwise
  creates it from the repository's current `HEAD`. An existing directory is
  reused as is. The command runs through `/bin/sh -c` on the server.
- `--slug` without `--worktree` is a usage error.
- `--no-mise` is accepted so existing scripts keep working. It has no
  effect.
- `--dry-run` connects, lists sessions, prints the requests `start` would
  send, and exits without changing anything.
- On Tuxosaurus the command runs through the server's login shell, so the
  user's shell profile applies, including mise if the profile activates it.
- By default the thread is kept after its command exits, so `read` and
  `status` still work and the exit code stays available. `--close-on-exit`
  lets the server remove it instead.

### send

```sh
rexctl send BOT RUN TEXT [--enter] [--enter-delay 150ms]
rexctl send BOT RUN - [--enter] < prompt.txt
```

`send` writes `TEXT` to the run's terminal. Quote `TEXT` if it has spaces;
extra arguments are a usage error. `-` reads the text from standard input.

No Enter key is sent by default. `--enter` sends Enter after the text, as a
separate write after `--enter-delay`. Some full-screen programs, including
Claude Code, treat text and Enter that arrive together as a paste and insert a
newline instead of submitting.

### key

```sh
rexctl key BOT RUN KEY...
```

`key` presses each named key in order. Names are case-insensitive, and both
tmux names and `ctrl+c`-style names work:

| Names | Sends |
| --- | --- |
| `Enter`/`Return`, `Tab`, `Esc`/`Escape`, `Space`, `BSpace`/`Backspace` | The usual bytes. |
| `BTab`, `shift+tab` | Back tab (`ESC [ Z`). |
| `Up`, `Down`, `Left`, `Right`, `Home`, `End` | Normal-mode cursor sequences (`ESC [ A`). |
| `PageUp`/`PPage`, `PageDown`/`NPage`, `Insert`/`IC`, `Delete`/`DC`, `F1` to `F12` | xterm sequences. |
| `C-c`, `ctrl+c`, `ctrl-c`, `control+c` (any of `a` to `z`, `@`, `[`, `\`, `]`, `^`, `_`, `?`, `Space`) | Control characters. |
| `M-x`, `alt+x`, `alt-x`, `meta+x`, `option+x` | Escape followed by the key. |
| any single character | That character. |

### read

```sh
rexctl read BOT RUN [--tail N] [--vt] [--no-trim] [--follow]
```

`read` prints the screen and scrollback as plain text, using the terminal
`format` method. `--tail N` keeps the last N lines; `--lines N` is an alias.
`--vt` keeps colors and other escape sequences.

`--follow` prints the screen, then streams new output until you press Ctrl-C,
the timeout passes, or the process exits. The stream is plain text with escape
sequences removed. With `--vt` it is the raw terminal output. Cursor movement
is not emulated, so programs that redraw the screen produce untidy text; use
plain `read` for an exact screen. A few lines around the start may print
twice.

### status

```sh
rexctl status BOT RUN
```

`status` prints one compact JSON line for the run, with or without `--json`:

```json
{"session_id":"session:...","session_label":"wes-bot","block_id":"block:...","label":"fix-login","state":"exited","reason":"child process exited","exit_code":0}
```

Fields: `state`, `reason`, `exit_code` (if the process exited), `title`,
`pwd`, `child`, `foreground` and `program` (the newest program status record).

`state` is one of `running`, `waiting`, `done`, `error` and `exited`,
decided in this order:

1. `exited`: the run's process has exited. `exit_code` is set.
2. The newest program status record. Programs report it through terminal
   escape sequences. Tuxosaurus also infers it for known tools such as
   Claude Code and Codex. `working` becomes `running`. `blocked` and `idle`
   become `waiting`. `done` becomes `done`. `error` becomes `error`.
3. `waiting` if a shell is the foreground process (the run is at a prompt).
4. `running` otherwise.

These are heuristics. A program that never reports status and does not exit
shows as `running` even when it is waiting for input. A script run as
`sh -c '...'` shows as `waiting` while it runs, because a shell is in the
foreground. Use `wait` (which waits for exit), `wait --until idle` or
`wait --pattern` in those cases.

### wait

```sh
rexctl wait BOT RUN [--for 30m] [--until exit|done|idle|pattern] \
  [--pattern RE] [--idle SECS] [--exit-status]
```

`wait` blocks until the run's command exits, then prints the status as one
JSON line with three more fields: `until` (the condition), `met`, and
`match` for a pattern. It exits 0 when the condition is met and 124 on
timeout, after printing the status with `"met":false`.

`--for DUR` is the timeout, as `30m`, `90s` or plain seconds. It is the same
as the global `--timeout`. Without either, `wait` waits indefinitely.

`--until` picks another condition:

| Condition | Met when |
| --- | --- |
| `exit` (default) | The process exited, or the run was removed. |
| `done` | `state` is `done`, `error` or `exited`, or the run was removed. |
| `idle` | No output for `--idle` seconds (default 5), or the process exited. |
| `pattern` | The Go regular expression `--pattern` matches the plain screen text. `^` and `$` match at the start and end of each line. `--pattern` alone implies this condition. |

`wait` learns of a process exit from the server's `child_exited` event, the
exit marker on the data stream, and by polling the process method every
second. If the run was removed and none of these reported an exit code,
`exit_code` is absent.

`--exit-status` makes `rexctl` exit with the run's exit code, when known.

### stop

```sh
rexctl stop BOT RUN [--signal int] [--grace 5s]
```

`stop` closes the run's window with `session.close_window`, which ends the
process in it. If that was the session's last window, `stop` also destroys
the session. With `--signal int`, `stop` first sends Ctrl-C and waits up to
`--grace` for the process to exit. With `--json` it prints `window_closed`
and `session_closed`.

### ls

```sh
rexctl ls        # sessions, with run counts
rexctl ls BOT    # runs in a session, with state
```

### close

```sh
rexctl close BOT
```

`close` destroys the session and every run in it.

### attach

```sh
rexctl attach BOT RUN [--detach-key KEY] [--resize take|advisory|off]
```

`attach` puts your terminal in raw mode, repaints the run's screen, then
shows live output and forwards your keystrokes.

- Press Ctrl-\ to detach. `--detach-key` takes any single-byte key name, such
  as `C-]`. The run keeps going.
- If the process exits, `attach` returns.
- `--resize take` (default) resizes the run to your terminal and follows
  window size changes. `advisory` records your size but resizes only if no
  other client owns the size. `off` never resizes. On detach, `rexctl`
  releases its size so another client's size can apply again.
- Each keystroke is one control request, so typing over a slow link lags.

## Examples

Run a one-shot Claude task in a new worktree and collect the result:

```sh
rexctl start wes-bot fix-login ~/src/app --worktree wes/fix-login -- \
  claude -p "Fix the login redirect bug and run the tests"
rexctl wait wes-bot fix-login --for 30m
rexctl read wes-bot fix-login --tail 80
rexctl stop wes-bot fix-login
```

The same with Codex, for a bot that checks the exit code:

```sh
rexctl start wes-bot lint-pass ~/src/app -- codex exec "Fix all lint errors"
rexctl wait wes-bot lint-pass --for 1h --exit-status
rexctl --json read wes-bot lint-pass
```

An interactive Claude session that a bot prompts and a person can join:

```sh
rexctl start wes-bot review ~/src/app -- claude
rexctl wait wes-bot review --until idle --idle 3
rexctl send wes-bot review "Review the diff on this branch" --enter
rexctl wait wes-bot review --until done --for 20m
rexctl attach wes-bot review     # Ctrl-\ to leave it running
```

## Exit status

| Code | Meaning |
| --- | --- |
| 0 | Success. |
| 1 | Error, including connection failures and server errors. |
| 2 | Usage error, including an ambiguous name. |
| 3 | Session or run not found. |
| 124 | Timeout. |

## Mac and Linux differences

`rexctl` has been run against both servers. `start`, `send`, `key`, `read`,
`status`, `wait`, `stop`, `ls` and `close` behave the same on each, with
these differences:

- Shell options. The Mac server's published options schema lists only
  `cwd`, but it also honors `command`: `CMD` runs as the block's child
  process and its exit code is reported. `start --worktree` relies on this.
- Interactive shells. With no `CMD`, the Mac server starts the shell under
  `login`, so the child process is `login`. `status` still reports `waiting`
  at a prompt, but the exit code after `exit N` is `login`'s, which is 0.
  Start the program as `CMD` when its exit code matters.
- Working directory. The Mac server does not expand `~` and falls back to
  the home directory when `cwd` is missing or invalid. See `start`.
- Screen format. The Mac server accepts only `text`, `html` and `vt` for
  `terminal.format`. `rexctl` asks for `text`, which Tuxosaurus also accepts.
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

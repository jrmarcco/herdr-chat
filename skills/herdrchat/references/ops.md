# Ops: running and looking after herdrchat

The bot (the single `herdrchat` binary) runs one of two ways:

- as a **service** of the system, after `herdrchat install` (launchd on macOS, systemd on
  Linux): it starts at login and comes back after a crash; recommended;
- in an **hc terminal** named `herdrchat` (`herdrchat-<profile>` for another profile), in
  the first session of `HERDR_SESSIONS`, after `herdrchat start` without a service.

`herdrchat status` says which, and where the profile's files are: settings in
`~/.config/herdrchat/<profile>/config.env`, state and logs in
`~/.local/state/herdrchat/<profile>/` (call it `D` below). The app's own settings (Feishu
scopes, callbacks) are in `references/feishu.md`.

## Is it running

```sh
herdrchat status                       # how it runs, sessions, keys set or missing, errors today
herdrchat logs --errors                # what went wrong today ("[feishu] connected" when in)
herdrchat start                        # restart: stops it everywhere, waits up to a minute until it is in
herdrchat install                      # make it a service: starts at login, back after a crash
launchctl print gui/$(id -u)/app.herdrchat.<profile>   # macOS: the service's state, pid, last exit
systemctl --user status herdrchat-<profile>            # Linux: the same
```

A restart is needed after any code or `config.env` change. It is safe: what was posted
is in `D/state.json`, so it picks up where it stopped.

While the bot is down the user hears nothing, so:

- Never end with the bot down: `herdrchat start` exits 1 with its errors when the bot
  does not reach its chat app within 60 s; fix that before anything else.
- Never stop or restart the hc core (`hc server stop|restart`) of the session the bot
  runs in without saying so first, and run `herdrchat start` right after.
- Never start an hc core from an agent's shell: the core passes that shell's variables
  (`CLAUDECODE`, `CLAUDE_CODE_*`) to every terminal, and an agent started there treats
  itself as a sub-agent (Claude Code then saves no transcript). Start it with those
  variables removed (`env -u CLAUDECODE -u CLAUDE_CODE_CHILD_SESSION … hc server start`).
- A crash posts "🔴 herdrchat is down" in the direct chat (at most every 10 minutes) and
  the next connection posts "✅ herdrchat is back" with how long it was gone.

## Logs

`D/logs/<YYYY-MM-DD>.log`, one file a day, 14 kept, lines `<time> <LEVEL> <text>`. Run as a service, what the process printed before its log was open (a crash at start) is in `D/logs/service.out.log`:

```sh
grep -h ERROR D/logs/*.log | tail -20   # what went wrong
grep -h WARN  D/logs/*.log | tail -20   # retries that recovered, lost /auth
grep -h ' <- \| -> ' D/logs/$(date -u +%F).log | tail   # posts out, your messages in
```

| Line | Means |
|---|---|
| `<- S/tN: 3 items (claude-code, live card)` | posted to a group, into the live card |
| `<- S/tN: blocked (4 options)` | a dialog card went out |
| `-> S/tN: picked 1. Yes` | a card button was pressed |
| `-> S/tN: <text>` | your message was typed into the terminal |
| `turn S/tN: done` | hc said a turn ended |
| `acting as you: on` / `off` | `/auth` token usable / lost |
| `uncaught:` + stack | the bot crashed; it was started again |

## State

`D/state.json` (never edit it while the bot runs: `herdrchat stop` first,
`herdrchat start` after):

| Key | Holds |
|---|---|
| `chats` | per terminal (`S|tN`, or `@machine|machine/tN`): group id, name, tags, label, muted, ended |
| `cursors` | per terminal: what was posted (ids, time), the open live card |
| `tags`, `labels`, `spaces` | ids of the bot tags, your labels, each space's avatar color |
| `overview`, `pinned`, `reacts` | the overview card, pinned groups, messages waiting for their OK |

Read only the keys you need (a JSON tool such as `jq`, or grep for one key).
`D/user-token.json` is your token after `/auth`: never print it.

## Usual problems

| You see | Likely cause | Do |
|---|---|---|
| nothing posts | bot down, or the owner not set (`FEISHU_OWNER_OPEN_ID`) | `herdrchat status`; the log says "owner not set": `herdrchat pair` |
| a button gives error 200340 | the card callback is not set up | `references/feishu.md`: events and card callbacks |
| a dialog had no card | answered in the terminal before the card; or no numbered menu on screen | log: `blocked`; the card says `/answer` when it saw no menu |
| the phone rings too much | Feishu: not `/auth`ed; either app: settings | Feishu `/auth`; `HERDR_MUTED_SPACES` (quiet but for needs you and done), `HERDR_SILENT_SPACES` (nothing rings), per profile |
| a ⚠️ warning in the direct chat about acting as you | the token could not be refreshed | send `/auth` again |
| a group keeps an old name | its terminal is not live | it renames when the terminal is back |
| a Feishu call fails with 400 in a burst | a rate limit (tags: 5 a second) | space the calls; the bot queues tag calls |
| `herdrchat start` says it did not reach its chat app | wrong keys, or no network | `herdrchat logs --errors`; check the keys in `config.env` |

## Reading an error

The bot says errors the same way everywhere — in a chat card, in a log line, at the
command line — so one table answers all of them. Find the text (or the part of it) and
do what the row says; anything not in the table is an unknown error: get the full line
with `herdrchat logs --errors` and treat it as a bug report, not a settings problem.

| The bot says | It means | Do |
|---|---|---|
| "this terminal has ended" | the terminal finished its command; nothing accepts input anymore | nothing; its group keeps the history (`ended` tag) |
| "hc cannot reach this terminal's machine; its room waits" | the machine behind an `@machine` terminal is offline or cut off | nothing on the bot side; the room resumes when the machine is back, it is not lost. `hc machine ls` says which link is down |
| "the agent started a new conversation; following that one" | `/clear` or a resume: the old transcript's rest was posted, the new one is followed | nothing |
| "the agent's own record cannot be read" | the agent's transcript/log file is missing or broken where the bot looked | `herdrchat logs` for the path it tried; check the agent still runs and its files are readable; if it repeats, capture the path and report it |
| "two agent sessions look alike here; it cannot be told which one this terminal runs" | two sessions of the agent in one folder cannot be told apart | start one of them in another folder, or accept the screen-only view for that terminal |
| "⚠ log match ambiguous" | Codex: the heuristic found more than one candidate log; a likelier one may exist | nothing fatal: what posts still comes from one log. If it mis-mirrors, restart that Codex in a folder of its own |
| "herdrchat's hook is not on this machine; reading the screen instead" | a remote machine has herdrchat's hook missing, so dialogs are read off the screen | to get dialogs in the agent's own words there: install herdrchat on that machine and `herdrchat hooks install` there |
| "this agent's conversation cannot be read; showing its screen" | the agent has no reader yet (pi, …); only its screen is mirrored | nothing to fix; typing gets the screen back |
| "the dialog has moved on; the card was refreshed" | the card you tapped was no longer the dialog on screen; a fresh card shows the current one | tap on the new card |
| "the bot does not know its owner yet" | no owner in `config.env` (fresh install) | `herdrchat pair` |
| "the bot is not running yet" | a step that needs the bot (pairing) ran while it was down | `herdrchat start`, then the step again |

## Capturing a screen sample

The screen-menu parser (`hc read` output → dialog buttons) is locked down by real
captures in `testdata/screens/`. When a dialog parses wrong (options missing, a wrong
key, a preview frame confused with an option), capture the screen before anything else:

```sh
hc read <session>/<address> --lines 40 > testdata/screens/<agent>-<what-it-is>.txt
```

- capture it while the dialog is on screen (40 lines cover the menu; the test suite
  parses every file in the folder, so a new capture gets a golden check for free);
- name it after the agent and what it shows: `claude-permission-write.txt`,
  `codex-plan-question.txt`;
- reproduce with the corpus test (`go test ./internal/core -run TestScreenCorpus -v`)
  and fix the parser against the capture; a CI golden failure is a behavior change
  signal, not noise.

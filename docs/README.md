<div align="center">

# herdrchat

The chat client for hc ([herdr](https://herdr.dev)): see and answer every hc terminal from Feishu.

[![CI](https://github.com/jrmarcco/herdr-chat/actions/workflows/ci.yml/badge.svg)](https://github.com/jrmarcco/herdr-chat/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/tag/jrmarcco/herdr-chat)](https://github.com/jrmarcco/herdr-chat/releases)

[English](README.md) · [简体中文](../README.md)

</div>

## What it is

**herdr-app is hc's desktop and phone client; herdrchat is hc's chat client.**

[herdr](https://herdr.dev) runs your coding agents (Claude Code, codex, pi, …) in terminals
that stay alive, and herdr-app shows them in spaces and tabs. But to use herdr-app away from
your desk, your phone has to reach the machine that runs hc first, and working with an agent
on a small screen is clumsy.

herdrchat shows the same terminals in a chat app you already have. It **connects out** to
Feishu by itself, so there is nothing to set up on your network: no public IP, no VPN. Each
herdr-app tab gets its own Feishu group: you read the agent as it works, tap buttons to
answer its questions, type and send images into the terminal, and see everything in one
overview card.

What it does:

- **Live cards**: one `⌛️ Working…` card per turn, edited every 5 seconds with what is new
  (tools it calls, your prompts, images) — an edit is not a message, so the phone stays
  quiet; the turn's last answer arrives as its own message;
- **Dialogs, three ways**: what the agent itself reports (its hook, its API) first — its own
  words, its own options as buttons; the screen's numbered menu next (a screen with no numbers
  but its own "Enter to continue · Esc to cancel" offer gets synthesized buttons that press
  exactly the keys it names); the whole screen with
  `/answer`, `/enter` and `/esc` as the fallback. The screen is re-read before any key is pressed, so
  an old card can never answer a newer dialog;
- **Input both ways**: typing in a group goes into the terminal; images are placed on the
  terminal's machine with `hc put` and their path typed in beside your words;
- **Overview and commands**: `/ls`, `/new`, `/kill`, `/clean` in the direct chat;
- **A footer under each post**: `claude · claude-opus-5-5 · main · context 23% (234k) ·
  5-hour 2% · 7-day 57%` — harness, model, branch, context and account limits, gathered by
  herdrchat itself at no model cost;
- **Nothing is lost**: what was posted is remembered by entry id (`state.json`), so a
  restart picks up where it stopped and the live card goes on being edited; a new group
  starts with the conversation before it (a file plus a folded post);
- **As a service**: `herdrchat install` makes it a launchd / systemd user service that
  starts at login and comes back after a crash; `herdrchat update` replaces the binary and
  restarts onto it.

Agents: **Claude Code, Codex and OpenCode** are mirrored fully (conversation, dialogs,
footer). Any other program (pi, a shell) answers with its screen until it has a reader.

## Quick start

### You need

- **herdr**: herdrchat is built on it and does nothing without it. Install it from
  [herdr.dev](https://herdr.dev) first (or let your coding agent do it with you:
  `/herdrchat setup` offers to); its **hc** must run on this machine with at least one
  session (`ls ~/.local/state/herdr-core`).
- **macOS or Linux**: a single binary; nothing else is needed.
- **A Feishu team** (not Lark): one where you may create a custom app and publish it.

### Install

```bash
# a release tarball (macOS/Linux, x86_64/aarch64) from
#   https://github.com/jrmarcco/herdr-chat/releases
# …or, with Go installed:
go install github.com/jrmarcco/herdr-chat/cmd/herdrchat@latest
```

### Set up

```bash
herdrchat setup     # asks its way through: checks the keys, picks sessions, starts, pairs
```

Or ask your agent: `/herdrchat setup` (the skill in [`skills/herdrchat/`](../skills/herdrchat/)
clicks the Feishu console for you with the Ego Lite browser). The manual steps (the app,
the keys, the console's long-connection events and callbacks, publishing, pairing) are in
[guide.md](guide.md#set-up-feishu).

Pairing: `herdrchat pair` shows a 6-digit code; send it to the bot in Feishu and it makes
you its owner and starts again.

With Claude Code or Codex, `herdrchat hooks install` is recommended: their questions and
permissions come to the chat **in their own words and options**, and your tap goes straight
back ([details](guide.md#answering-a-dialog-from-the-chat)).

Coming from the npm (Node) version? Stop the old one, put this binary where the
`herdrchat` command is, `herdrchat start`: [migrating](guide.md#migrating-from-the-node-version).

## Use

### In the chat

| Direct chat (with the bot) | Does |
|---|---|
| `/ls` | the overview again (every terminal, what waits for you, links) |
| `/new <session> <space>/<tab> [command]` | start a terminal (default `claude`; `--cwd <dir>` first) |
| `/kill <session> <address>` | end a terminal |
| `/clean` | clean up ended terminals' groups and stale files (asks first) |
| `/auth` | let the bot act as you: mute groups, labels, pins |

| A terminal's room | Does |
|---|---|
| type | goes into the terminal |
| send an image | placed on its machine, its path typed in beside your words |
| `/commands` | the slash commands this agent really has here (tap to send) |
| `/screen` `/answer <text>` `/enter` `/esc` | the screen / type as-is / press enter / press esc |
| `/history` | the conversation so far (a file and a folded post) |
| card buttons | answer its questions (multi-select, free text, Esc) |

### On the command line

```
setup      set a profile up: the chat app, its keys, the sessions, the first start
pair       hand the bot to its owner with a 6-digit code
init       write a starter config.env
start      start the bot (a service, or a terminal that keeps it up)
stop       stop it
status     say whether it runs, and what is wrong if anything
logs       its log, --errors for the problems, --day for another day
install    keep it running as a system service
uninstall  remove the service
hooks      show, install or remove the PermissionRequest hook
update     bring in the newest herdrchat, and restart the bot onto it
run        run the bot in the foreground
version    say which herdrchat this is
```

`--profile <name>` runs a second setup beside the first.

### Update

```bash
herdrchat update     # latest release → tar.gz + checksums → sha256 → atomic swap → hot restart
```

Installed with `go install`? `go install …@latest`, then `herdrchat start` per profile.
The bot itself tells the owner when a new release is out (it never self-upgrades).

## Architecture

### The flow

```
                 Feishu (larkws, an outbound long connection: no public exposure)
                  ⇅ messages · cards · buttons · reactions · offline catch-up
        ┌─────────────────────────────────────────────┐
        │ internal/chats/feishu                        │
        │ (the Chat contract: card schema 2.0, groups, │
        │  /auth device flow, catch-up, rate limits)   │
        └──────────────────┬──────────────────────────┘
             the chats.Chat contract (internal/chats/chat.go)
                  ⇅
        ┌─────────────────────────────────────────────┐
        │ internal/engine (the orchestration)          │
        │  rooms      a room per tab, its lifecycle    │
        │  posting    the pipeline, live cards         │
        │  dialogs    three answer paths, stale-proof  │
        │  intake     what you typed, hook answers     │
        │  overview   one card, edited in place        │
        │  · one serial lane per terminal              │
        └──┬──────────┬──────────────┬───────────────┘
   the readers.Reader contract │       │ the HC contract (core.HC)
      ┌───────────────┤              ├─────────────────┐
      ⇅               ⇅              ⇅                 ⇅
 readers/claude   readers/codex  readers/opencode   internal/hccli
 (transcript +     (rollout log,   (opencode api:    (the hc CLI: event
  /context /usage)  heuristics)     sessions, forms)  stream, typing,
                                                       screens, exec --on)
      ⇐———— each reads the agent's own records; nothing installed in the agents ————⇐
                                                       ⇅
                                              hc (herdr-core) terminals,
                                              this machine + connected ones
```

A side path: `herdrchat hooks install` puts the embedded `ask.sh` (byte-for-byte the
upstream one) into Claude Code's and Codex's settings → a question lands in
`~/.local/state/herdrchat/asks/` and labels the terminal `herdrchat.ask=<id>` → the engine
posts the card and writes your tap back as the decision. The protocol is unchanged from the
Node version, so both versions can hand over in place.

### Two plugin seams

- **Chat apps** (the `Chat` interface in `internal/chats/chat.go`, 29 members): the engine
  treats every app the same; what only an app has (Feishu's tenant tags, avatars, acting as
  you) stays in its own package. The core never imports an app; an app never imports the
  engine.
- **Agents** (the `Reader` interface in `internal/readers/reader.go`, plus small optional
  ability interfaces — dialogs, answers, meter, titles): hc's `status.app` picks the
  reader; the foreground process is checked, so a leftover status mislabels nothing.

The engine gets hc, the readers, the store and the chat injected, so the tests swap them
for the `internal/fake` trio and run every behaviour without a socket.

### Concurrency and reliability

- **Lanes**: one serial FIFO per terminal key — ordered side effects per terminal, no
  cross-terminal waiting;
- **The cursor**: dedupe by entry id and timestamp, a 10-minute lag against late writes; a
  failed post never advances it — **a repeat is acceptable, a loss is not**;
- **State**: `state.json` read-merge-write whole, swapped by atomic rename; a corrupt file
  is kept as `corrupt-<ts>` and reported, never silently dropped;
- **Crash recovery**: a panic logs its stack → the owner gets a 🔴 card → the service or
  the terminal loop starts it again → a ✅ card when it is back;
- **Every cache is bounded**: images 500, dialog cards 200, buttons 300, seen 2000.

### Code index

One Go module, ~22k lines; the only runtime dependency is the official Feishu SDK
(`github.com/larksuite/oapi-sdk-go/v3`).

| Path | What it is | Key files |
|---|---|---|
| `cmd/herdrchat/` | process-level entry: assembles the CLI, ignores SIGPIPE, logs panics | `main.go` |
| `internal/cli/` | every subcommand; all seams injected (env/home/TTY/clock/HC/service) so tests never touch the real machine | `cli.go` (dispatch+usage), `run.go` (the bot wiring), `setup.go`, `update.go`, `start.go`, `install.go`, `status.go`, `logs.go`, `pair.go`, `initcmd.go`, `hookscmd.go` |
| `internal/engine/` | the engine: the same for every chat app and agent | `engine.go`, `rooms.go`, `posting.go`, `dialogs.go`, `intake.go`, `overview.go`, `pairing.go`, `commands.go`, `clean.go`, `update.go` |
| `internal/core/` | pure types and algorithms | `types.go`, `errors.go` (error taxonomy), `lanes.go`, `cursor.go`, `menu.go` (screen-menu parsing), `views.go` (multi-view topology), `meter.go`, `hc.go` (the HC contract) |
| `internal/chats/` | the chat-app seam | `chat.go` |
| `internal/chats/feishu/` | Feishu: larkws, cards, API calls, /auth | `chat.go`, `cards.go`, `sdk.go`, `userauth.go` |
| `internal/readers/` | the agent seam + registry | `reader.go` |
| `internal/readers/claude/` | reads `~/.claude` transcripts; `/context`, `/usage` for the footer | `claude.go`, `ask.go` |
| `internal/readers/codex/` | reads `~/.codex` rollout logs (heuristic); Plan/async questions | `codex.go`, `questions.go` |
| `internal/readers/opencode/` | talks to `opencode api` (sessions, messages, forms, permissions) | `opencode.go`, `dialog.go` |
| `internal/hccli/` | the hc CLI wrapper: a deadline on every call, `HC_TERMINAL_*` stripped, 16MB cap; the event-stream follower (10MB buffer); capability probe | `hc.go`, `events.go`, `home.go`, `capability.go` |
| `internal/asks/` | the hook's asks protocol (req/ans files, the label, stray claims) | — |
| `internal/hooks/` | `herdrchat hooks`: ask.sh embedded byte-for-byte; touches only marked entries; backup + atomic writes | — |
| `internal/service/` | launchd/systemd user service; env whitelist | `service.go`, `templates.go` (embeds the plist/unit skeletons in `templates/`) |
| `internal/config/` | profile placement (XDG/HERDRCHAT_HOME), config.env parsing and line-wise edits | — |
| `internal/state/` | state.json, schema-compatible shapes, atomic writes, corrupt-file backup | — |
| `internal/cliversion/` | the one version variable goreleaser stamps via `-X` | — |
| `internal/xlog/` | one-line logs, daily files, 14 kept, TTY colors | `handler.go`, `daily.go` |
| `internal/fake/` | the test trio: FakeChat / FakeHC / FakeReader | — |
| `testdata/screens/` | 28 real screen captures locking `menu.go` down (new captures join the golden test) | — |
| `scripts/` | the two lint tiers (CI-blocking / report-only), git hooks | `lint/.golangci.yaml` |
| `skills/herdrchat/` | the agent skill: `/herdrchat setup/status/fix/…` | `SKILL.md`, `references/` |
| `.goreleaser.yaml` + `.github/workflows/` | the release path: tag `v*` → CI (vet+race) → goreleaser, four platforms (what `herdrchat update` downloads) | — |

Everyday commands: `make lint` (the CI-blocking tier), `make lint-strict` (report-only),
`go test -race ./...` (15 packages green is the bar to merge).

### Extending

**Adding a chat app**:

1. Read the app's docs first, line by line against the contract: how a room is made, how a
   message is edited, buttons and callbacks, reactions, images both ways, how the phone is
   kept quiet, the rate limits; write down what it cannot do and what stands in for it;
2. Make `internal/chats/<app>/` satisfying `chats.Chat` (compile-time check:
   `var _ chats.Chat = (*Chat)(nil)`), keep its API calls in a `sdk.go` beside it, pass
   `chats.CheckChat` at startup;
3. Give it a `Requires` list (the config.env keys it cannot start without) and use it in
   `internal/cli`'s `ready()`;
4. Add its keys to the root `config.env.example`;
5. Tests: `CheckChat`, plus its own message conversions;
6. Add `skills/herdrchat/references/<app>.md` (the console steps);
7. Run it in its own profile (`--profile <app>` with `HERDRCHAT_CHAT=<app>`), so the
   running bot is not touched.

**Adding an agent**:

1. Learn how it keeps its conversation on disk: where the log is, how to find the one for
   a running terminal, how a turn's last answer is marked;
2. Make `internal/readers/<agent>/`, wire the shared pieces (files, shell, the asks
   instance, the meter cache), implement the `Reader` contract (`App`, `Processes`,
   `After`, `Rest`); dialogs, answers, meter and titles are optional small interfaces;
3. Register it in `internal/cli/run.go` (`CheckReader` + `Registry.Register` check it);
4. Add tests with a sample log;
5. Add a row to the readers table in `docs/guide.md`.

## Documents

| Document | What it holds |
|---|---|
| [guide.md](guide.md) | **the user guide**: every setting, rooms in detail, the three dialog paths, the Feishu console by hand, migrating, /auth, limits |
| [../README.md](../README.md) | this README in 简体中文 |
| [history.md](history.md) | **the rewrite's record**: goals, decisions and milestone outcomes of the Node→Go rewrite (a distillate of the six planning docs; the full texts live in git history) |
| [../skills/herdrchat/](../skills/herdrchat/) | the agent skill: say `/herdrchat` once installed |
| [../THIRD-PARTY-NOTICES](../THIRD-PARTY-NOTICES) | attribution: zhangxiangliang/herdrchat (MIT), cobanov/herdrchat (Apache-2.0), the Feishu SDK |

## Privacy and safety

- What your terminals show goes to Feishu's servers.
- Only the owner set in `config.env` can type into your terminals; everyone else is ignored.
- Keep the Feishu app secret like a password: whoever has it can drive your terminals.
- The bot's tags are visible to your whole team (`HERDR_BOT_TAGS=off` turns them off).
- With the hooks installed, a tap in the chat can allow what Claude Code or Codex asks to
  do, as a key in the terminal can.

## Remove

`herdrchat uninstall` per profile → `herdrchat hooks uninstall` → remove the binary
(`command -v herdrchat` says where) → delete `~/.config/herdrchat` and
`~/.local/state/herdrchat` → delete the Feishu app.

## License

MIT © [zhangxiangliang](https://github.com/zhangxiangliang) (the original's author). This
is its Go rewrite; what was borrowed from where: [THIRD-PARTY-NOTICES](../THIRD-PARTY-NOTICES).

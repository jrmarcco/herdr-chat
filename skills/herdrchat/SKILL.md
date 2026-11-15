---
name: herdrchat
description: >-
  Set up, run and look after herdrchat, the chat client for hc (herdr), which mirrors every herdr-app tab into
  Feishu (a group per tab): first setup of the app,
  start, stop, status, logs, running it as a service, settings, fixing problems and the
  Feishu developer console. Use for anything about herdrchat or its Feishu bot, even
  "is it running", "it does not post" or "mute the workers". Trigger with
  "/herdrchat", "/herdrchat setup",
  "/herdrchat status", "/herdrchat fix".
---

# herdrchat

herdrchat mirrors every herdr-app tab into a chat app: a group per tab in Feishu, a topic
per tab; the conversation as it happens, dialogs as
buttons, an overview in the direct chat. One profile is one chat app (`HERDRCHAT_CHAT`):
one app uses the default profile; a second app beside it gets its own (`--profile <name>`).

Its command is `herdrchat`, a single binary: check with `command -v herdrchat`; when
it is missing, ask before installing it (a tarball from the repo's Releases, or
`go install github.com/jrmarcco/herdr-chat/cmd/herdrchat@latest`). A profile
(`--profile`, default `default`) keeps its settings in
`~/.config/herdrchat/<profile>/config.env` and its state in
`~/.local/state/herdrchat/<profile>/`; `herdrchat status` prints both. Files named
here (`config.env.example`, `docs/guide.md`) live in the repo beside this skill
(`config.env.example` at the repo root, `docs/`).

## Commands

| Command | Do |
|---|---|
| `/herdrchat setup` | First check herdr is there (`command -v hc`): herdrchat is built on herdr and does nothing without it. When it is missing, offer to install it and, on a yes, follow `references/herdr.md` (from herdr's own instructions, read now, each command shown and agreed first). This build talks to Feishu only. Feishu, in this order (with ego-browser the agent clicks the console; without it, offer to install Ego Lite or walk the user through it, `references/feishu.md` "Before anything"): create the app, the bot ability and the scopes; `herdrchat init` and fill `config.env` with the user (App ID, App Secret; `HERDR_SESSIONS` empty means hc's default session, `ls ~/.local/state/herdr-core` lists the sessions); `herdrchat start` (the console's long-connection check needs it running); events and callbacks; publish a version; `herdrchat pair` (in the background: it prints a 6-digit code and waits; the user sends the code to the bot, which writes `FEISHU_OWNER_OPEN_ID` itself and starts again; it also proves the events work); offer `/herdrchat auth`, `herdrchat install` and, when Claude Code or Codex is used, `herdrchat hooks install` (their dialogs come to the chat in their own words; say first that it edits their settings, and that Codex then asks once to trust the hook in `/hooks`; never pass `--yes` without the user's yes). Never run `herdrchat setup` yourself: it asks questions in a terminal, and this shell has none. It is the same steps for a user without an agent; yours add the Feishu console through ego-browser. When there is no hc session yet, say so and stop until herdr runs one. |
| `/herdrchat status` | `herdrchat status`; explain each line; on errors, `/herdrchat fix`. |
| `/herdrchat start`, `/herdrchat stop` | Read `config.env` (its `HERDR_SESSIONS`) in one step, then `herdrchat start` / `stop` in the next; never both at once. With a service installed they go through it. |
| `/herdrchat install`, `/herdrchat uninstall` | Run it as a service of the system (launchd on macOS, systemd on Linux): it starts at login and comes back after a crash, whatever happens to the hc cores. Recommended once it works. Read the config first; after the binary is moved by hand, install again (`herdrchat status` says so). |
| `/herdrchat clean` | `/clean` in the bot's direct chat shows what a cleanup would delete (ended terminals' groups and stale screen files) as a card; tapping "Yes, delete them" dissolves the groups — this needs the `im:chat:delete` scope (`references/feishu.md` "Import scopes"). |
| `/herdrchat logs` | `herdrchat logs --errors` (or `-n 100`); explain them with `references/ops.md` "Logs". |
| `/herdrchat auth` | The user sends `/auth` to the bot and allows it on Feishu's page; the app needs the user scopes in `references/feishu.md`. Then groups are muted, labels and pins follow. |
| `/herdrchat config` | Change a setting in `config.env` with the user (sessions, `HERDR_MUTED_SPACES`, `HERDR_SILENT_SPACES`, `HERDR_BOT_TAGS`, `HERDR_USER_TAGS`, `HERDR_HISTORY`, `HERDRCHAT_CHAT`; each is explained in `config.env.example`), read the file back, then restart. |
| `/herdrchat fix` | Find the cause from `herdrchat status`, the logs and the live result, then fix it: `references/ops.md`. |
| `/herdrchat feishu <task>` | Anything in the Feishu developer console: scopes, callbacks, publishing, the icon, deleting an app: `references/feishu.md`. |

With no subcommand, run `herdrchat status` and suggest the next step from it.

## Rules

- Ask before anything outward-facing: publishing a version, applying scopes, deleting or
  disbanding anything, sending to a real group. One yes covers one action.
- Never print, screenshot or paste `FEISHU_APP_SECRET` or
  `user-token.json`; ask the user to paste a token into `config.env` themselves.
- A check that decides an action runs on its own first (read the config, then start).
- Say what was checked on the live bot and what was not.

## References

- `references/feishu.md`: the bot's app in the Feishu developer console, with Ego or as a
  guided walk-through; the scopes this bot uses.
- `references/herdr.md`: what herdrchat is built on, and installing herdr with the user
  when hc is missing.
- `references/ops.md`: running and checking the bot, its logs and state, the usual
  problems, what each of the bot's errors means and what to do about it, capturing a
  screen sample when a dialog parses wrong.

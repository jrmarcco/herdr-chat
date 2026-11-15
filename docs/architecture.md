# herdrchat architecture

herdrchat has two kinds of plug-ins that grow over time: **chat apps** (Feishu now,
Slack and Discord later) and **agents**, each read by its reader (Claude Code, Codex
and OpenCode now, then pi). The core between them knows neither. This page says where
everything is and how to add one of each.

Everything below is one Go module; the built product is a single `herdrchat` binary
(`cmd/herdrchat`), released with goreleaser and self-updated by `herdrchat update`.

## The parts

```
cmd/herdrchat/            process-level wiring: App assembly, SIGPIPE, panic log
internal/cli/             every subcommand (cli.go dispatch; run/start/status/pair/
                          update/hooks/install/setup.go …) — all seams injectable
internal/engine/          the same for every chat app and every agent
  engine.go               start/stop, event wiring, lanes, timers
  rooms.go posting.go     a room per herdr-app tab; what gets posted, live cards,
                          dialogs.go intake.go      dialogs and clicks, what you type
  overview.go             the overview in the direct chat
  pairing.go commands.go  handing the bot to its owner; the /commands catalogue
internal/core/            pure types and algorithms: cursor, menu, views, meter,
                          lanes, pairing codes, errors, the HC contract
internal/hccli/           hc: commands (hc.go), its live event stream (events.go),
                          files on the terminal's machine (home.go), capability probe
internal/asks/            what an agent asks through herdrchat's hook (its request,
                          your answer)
internal/hooks/           `herdrchat hooks`: the hook in Claude Code's and Codex's
                          settings; ask.sh is embedded here (byte-for-byte)
internal/service/         herdrchat as a launchd or systemd service
internal/state/           state.json (atomic writes, schema-compatible shapes)
internal/config/          placesOf / config.env parsing / Load
internal/xlog/            the log: one line, a level, a component; daily files
internal/readers/         one reader per agent (the contract is in reader.go)
  claude/ codex/ opencode/
internal/chats/           chat apps (the contract is in chat.go)
  feishu/                 chat.go (the app), cards.go, sdk.go (API calls), userauth.go
internal/fake/            a fake chat app, a fake hc and a fake reader (tests)
internal/cliversion/      the one version variable goreleaser stamps via -X
```

The rules that keep it this way:

- The core (`engine`, `core`, `readers`) never imports a chat app, and a chat app
  never imports the engine. They meet only through the contract in
  `internal/chats/chat.go`.
- Everything a chat app does is in its own package, including what only it has
  (Feishu's tags, avatars and acting as you).
- The engine gets hc, the readers and the store passed in, so the tests swap them
  for fakes (`internal/fake`). A behaviour of the engine gets a test in
  `internal/engine`.
- Words the core uses: a **room** is where one tab is mirrored (a Feishu group); `k`
  is the engine's key for a tab's terminal (`<session>|tN`, `@<machine>|…`); `room`
  and `message` are the app's own ids, which the engine keeps as given.

## Adding a chat app

1. Read the app's official docs first, for each line of the contract: how a room is
   made (a group, a channel), how a message is edited, buttons and their callbacks,
   reactions, images both ways, how the phone is kept quiet, the rate limits.
   Write down what it cannot do and what stands in for it.
2. Make `internal/chats/<app>/` with a type that satisfies `chats.Chat` (compile-time
   check: `var _ chats.Chat = (*Chat)(nil)`); keep its API calls in a `sdk.go`
   beside it and pass `chats.CheckChat` at startup.
3. Give it a `Requires` list (the config.env keys it cannot start without) and use
   it in `internal/cli` where `ready()` loads the config.
4. Add its keys to the embedded `config.env.example` (`internal/cli`).
5. Add tests: `CheckChat` passes, and tests for its own message conversions.
6. Add `skills/herdrchat/references/<app>.md`: setting the app up, step by step.
7. Run it in its own profile (`herdrchat start --profile <app>` with
   `HERDRCHAT_CHAT=<app>` in that profile's config.env), so the running Feishu bot
   is not touched.

## Adding an agent

1. Read how the agent keeps its conversation on disk, from its docs or its source:
   where the log is, how to find the one for a running terminal, how a turn's last
   answer is marked.
2. Make `internal/readers/<agent>/` with a `New(hc core.HC, …) *Reader` constructor
   wiring the shared pieces (files, shell, the asks instance, the meter cache), and
   implement the `readers.Reader` contract (`App`, `Processes`, `After`, `Rest`);
   optional abilities (dialogs, meter, titles) are separate small interfaces.
3. Register it in the engine's assembly (`internal/cli/run.go`); `readers.CheckReader`
   and `Registry.Register` check it there.
4. Add tests with a sample log.
5. Add a row to the readers table in `docs/guide.md`.

# herdrchat guide

How herdrchat works and every setting, for when the README is not enough.

herdrchat is hc's chat client, as herdr-app is its desktop and phone client: every
herdr-app tab, followed and answered in Feishu. It sits on one core (hc events, a
reader per agent, dialogs, the overview), with the chat app behind one contract
([README.md](README.md), "Architecture"); other apps may follow the same way. This
build talks to Feishu only (`HERDRCHAT_CHAT`).

Words used here:

| Word | Meaning |
|---|---|
| hc, herdr-core | the program that runs terminals and keeps them alive ([herdr.dev](https://herdr.dev)) |
| herdr-app | its desktop and phone app, which shows the terminals in spaces and tabs |
| session | one hc core and its terminals (`HC_SESSION`); `ls ~/.local/state/herdr-core` lists those on a machine |
| space, tab | how herdr-app groups terminals in its sidebar |
| room | where one tab is mirrored: a Feishu group |
| direct chat | your private chat with the bot: the overview and the commands |
| profile | one herdrchat setup for one chat app, with its own settings and state (`default` unless named) |
| reader | the part that reads an agent's own conversation log (Claude Code so far) |

## Feishu

herdr-app, moved into one Feishu bot:

| herdr-app | Feishu |
|---|---|
| a tab | a group named `<space> / <tab>`, tagged with its session; type there to talk to it |
| a space | one avatar for all of its groups: the space's first letter on a color of its own |
| a terminal on another machine | a group named `<space> / <tab>`, tagged with the machine, made once |
| a terminal that is gone | its group stays, with its history, tagged `ended` (untagged if it comes back) |
| the sidebar | the direct chat with the bot: an overview card (every terminal's state, what waits for you first, each a link to its group), kept up to date in place; `/ls`, `/new`, `/kill` |

A tab herdr-app only numbered (`2`) is named after the terminal's title instead (what the
program sets; Claude Code sets its session name or topic), else its hc name.

Everything comes from hc, so it works the same for Claude Code, codex, pi or any program
hc shows, on this machine or on a machine hc has added. Nothing is installed in the agents.

## Settings

In the profile's `config.env` (`herdrchat status` prints where it is):

| Key | Default | What it does |
|---|---|---|
| `HERDRCHAT_CHAT` | `feishu` | the chat app this profile mirrors into; this build supports `feishu` only |
| `HERDR_SESSIONS` | `default` | the hc sessions to mirror, comma-separated; machines they reach come along. `default` is hc's own session, which with herdr-app reaches every session it shows |
| `HERDR_MUTED_SPACES` | `all` | spaces whose rooms are quiet: only "needs you" and "done" ring; empty for none |
| `HERDR_SILENT_SPACES` | (none) | spaces where nothing rings, not even "needs you" |
| `HERDRCHAT_SPACE` | `dev` | the space name for terminals started from chat (`/new`) when they have no sibling to follow |
| `HERDR_HISTORY` | `file,folded` | what a new room gets of the conversation before it: `file`, `folded`, both, or empty |
| `FEISHU_APP_ID`, `FEISHU_APP_SECRET` | (required) | the Feishu app |
| `FEISHU_OWNER_OPEN_ID` | (empty) | you; the bot tells you yours when you message it |
| `FEISHU_ENCRYPT_KEY`, `FEISHU_VERIFICATION_TOKEN` | (empty) | optional, from the console's 事件与回调 → 加密策略 page. The long connection works without them; set them and events that arrive encrypted are decrypted |
| `HERDR_BOT_TAGS` | `on` | the session or machine tag and `ended` next to a group's name |
| `HERDR_USER_TAGS` | `on` | your sidebar labels, once `/auth` is done |

Where a profile's files are: settings in `~/.config/herdrchat/<profile>/config.env`,
what the bot writes (state, logs, your `/auth` token) in
`~/.local/state/herdrchat/<profile>/`; `herdrchat status` prints both.

In the environment (not `config.env`): `HERDRCHAT_PROFILE` (or `--profile`) picks the
profile; `HERDRCHAT_HOME` puts a profile's `config.env` and state in one folder of your
choice (give each profile its own); `XDG_CONFIG_HOME` and `XDG_STATE_HOME` move the usual
folders. A setup in a moved folder gets its own hc terminal and service name, so trying
one never touches the usual one. A key set in the environment wins over `config.env`. A
setting changed in `config.env` takes effect at the next `herdrchat start`.

## How it works: hc, plus a reader per agent

**hc is the backbone.** The bot follows `hc events --json` for each session (its format:
`hc schema`). From hc it learns which terminals exist, where
herdr-app shows them, their status, and when a turn ends (`turn` event: the status left
`working`). Typing from a group goes through `hc send`.

**A reader gives the conversation.** hc can show only the screen, and the screen is a
picture: long output is folded, and you cannot tell your words from the agent's. So for
each agent a reader reads the agent's own records. The status hc reports names the agent
(`status.app`), and that name picks the reader (`internal/readers/`):

| `status.app` | reader | reads |
|---|---|---|
| `claude-code` | `internal/readers/claude` | its transcript: the terminal's foreground pid names the session in `~/.claude/sessions/<pid>.json` |
| anything else | none | nothing by itself; what you type gets its screen in reply |

On another machine, a reader reads the same files through `hc exec --on`. To add an
agent: [README.md](README.md#extending), "Adding an agent".

## What a room shows

A room is a Feishu group.

- A program with a reader:
  - while it works: one "⌛️ Working…" card per turn (then "✅ Worked"), edited every 5
    seconds with what is new: what it says, your prompts (typed in the terminal or sent
    mid-turn) as **You:**, and each tool it calls as one line (`▸ Bash: Run the tests`).
    It shows the program is alive, as herdr-app does, and since an edit is not a new
    message, the phone does not ring every few seconds;
  - when the turn ends: its last answer, as a new message. In a space of
    `HERDR_MUTED_SPACES` (all, by default) only "needs you" and "done" ring: the
    groups are muted (after `/auth`) and an @ under the answer rings.
    `HERDR_SILENT_SPACES` rings nothing.
  - A message another terminal sent with `hc send` shows as **From t7 review**, not as
    yours (hc wraps it in `<hc from="…">`, for any agent).
  - Images too: ones it looked at (a screenshot, an image file it read) and ones you
    pasted in the terminal, shown in the card.
  - What you sent from the group is not repeated.
  - Under each post and card, one small line says where it came from and how it stands:
    the harness hc names, the model the agent's own records name (or `screen` when it was
    read off the screen), the git branch of its folder, how full its context is, and the
    account's limits:
    `claude · claude-opus-5-5 · main · context 23% (234k) · 5-hour 2% · 7-day 57%`.
    herdrchat adds it, never the agent, and spends nothing on it: Codex writes all of it
    in its own log; Claude Code's window and limits come from `claude -p /context` and
    `claude -p /usage`, which Claude Code answers itself without asking a model (the
    limits at most every 5 minutes per machine); OpenCode's window from its model list.
    What is not known is left out: limits for an API key, or OpenCode's plans, which
    name none.
- Anything else (a shell, a tool, an agent that has quit): nothing by itself. A terminal
  counts as an agent only while its foreground process is that agent, so a status left
  over after it quits posts nothing.
- A turn that ends on `blocked` (permission, question, auth): what it said first, then
  a red "⚠️ Needs you" card. When the screen shows a numbered menu (as Claude Code's
  permission and question dialogs do), the card shows only the dialog, as text, with a
  button per option; a multi-select also has "Next ⇥", a "Type something" option a box to
  type in, and every card "Esc".
  Without a menu the card shows the last lines of the screen and how to answer by typing.
  Once the dialog is gone the card turns to "✅ Done". A card is posted only for a dialog
  that still waits: one answered while its screen was read gets none.

## Answering a dialog from the chat

### From the agent itself, when it can say

When the agent can tell herdrchat what it asks, that comes first, and no screen is read:
OpenCode through its own API (`opencode api`: its forms and permission requests), Claude
Code and Codex through herdrchat's hook in their settings (`herdrchat hooks install`,
`lib/hooks/ask.sh`, their PermissionRequest hook). The hook leaves the request in
`~/.local/state/herdrchat/asks/` on the machine the agent runs on and labels its terminal
`herdrchat.ask=<id>` while it waits; that label (an hc event, from a connected machine
too) brings the card, with the agent's own words and options. Codex run through its
shared background service (without `--no-daemon`) runs its hooks there, outside the
terminal: the request then names no terminal, and herdrchat finds it by Codex's session
(the one the terminal's log is of) and labels the terminal itself, every 2 seconds on
this machine and every minute on a connected one. Your tap is written back as
the hook's decision: Claude Code's permissions (Yes, Yes and don't ask again when it offers
rules, No), its questions (one card per question, boxes for a multi-select, your own words;
the answers go back together) and its plan (start, start and accept its edits, keep
planning); Codex's commands, patches and MCP tools (Yes, No, Answer in the terminal). Esc
on a card stops Claude, as in its own prompt; for Codex it says no.
The terminal still works: Claude shows its prompt as usual, and an answer there closes
the card. Codex shows its prompt only once the hook is done: "Answer in the terminal"
hands it back, and so does the time running out (10 minutes; 30 for Claude). Once the
hook is done the card closes, and a dialog still on the screen gets a card of its own.
Codex's questions come from its own log (no hook is needed for them). In Plan mode it
asks with `request_user_input` and waits: the card has its words (the header, the
question, each option's note, "question 1 of 2"), one card per question, plus "None of the
above" with your own words; a tap presses that option's number in its prompt (your own
words: down to "None of the above", tab, the words, Enter), once the screen shows that
very question. Its other way, `request_user_input_async`, does not wait: the turn ends,
and your answer is just your next message. Its card stays while Codex is idle; your taps
(one per question) go as one message ("Which fruit? Pear", one line each), and anything
you type in the group answers it too. Once Codex works again the card closes.
A dialog the agent does not name this way (and every dialog without the hook) is read off
the screen, below.

### From the screen

A button presses that option's number key in the terminal (Esc presses esc). A screen with
no numbered choices but its own "Enter to continue · Esc to cancel" line gets one synthesized
Continue button (plus the card's Esc); the tap presses the key the screen itself named, never
a digit. Cursor-choice screens (a ❯ on an option) are never given buttons — Enter would
confirm whatever the cursor points at. A question
with previews (a box beside its options) takes no number, so there the button moves with
the arrows from the cursor to that option and presses Enter. In a multi-select, a box is
ticked on the card only, at once; Next sends them all: each box that differs from the
screen is pressed, then tab.
Before it presses anything, the bot reads the screen again and checks that it still
shows the same dialog (a hash of it, from its top border to its last option, without the
cursor, the check marks and the preview box) and the same option, so an old card can never answer a newer dialog that looks alike. After the
key, the card turns into the dialog's next step (a toggled box, the next question, the
review) or says what was picked. Text you type in the group while it waits goes into its
"Type something" box when it has one.

A dialog answered in the terminal (or left for the next one) closes its card: the
buttons go and the card says so. The screen is read the moment `blocked` arrives, so a
dialog answered in the terminal at once still gets its card; and since hc sends a turn
event only when a program leaves `working`, status events catch a program that goes
from one dialog straight to the next. One dialog gets one card.

## Nothing is lost

- What was posted is remembered per terminal in `state.json` (entry ids, not only a
  time), so a restart picks up where it stopped, entries with the same timestamp or
  written late are still posted, and nothing is posted twice.
- A post that fails is retried (2, 4, 8, 16 s); if it still fails the cursor stays, and the
  content goes out with the next post. A repeat is acceptable, a loss is not.
- The "⌛️ Working…" card holds everything, not a status line. A full card (20 KB) is closed
  and the next one goes on; a card that cannot be edited is sent again as a new one. Which
  card is open is kept in `state.json`, so a restart goes on editing it. While it
  works, the turn's last answer is left for the turn's end, so it always arrives as its
  own message.
- After a turn ends the bot waits for the agent's last answer (for Claude,
  `stop_reason: end_turn`); hc reports the end before the agent writes it.
- Idle terminals with a reader are checked again at start, after every event snapshot and
  every minute, so a missed event or a late write is caught up.
- When a Claude session moves to a new transcript (`/clear`, resume), what is left in the
  old one is posted first; when a terminal ends, what its log still holds is posted first.
- Some of what Claude says while it works is stored only as a "narration" block (a thinking
  block whose signature names narration); it is posted too. It holds a condensed version of
  the text shown on screen, the only version on disk.

On the first start the bot posts only what happens from then on, never older history.
A new room is not empty: it gets the conversation before it, all of it as a `.md` file
and its newest part in one folded post, silent and never repeated (`HERDR_HISTORY` says
which: `file,folded` by default, `file`, `folded`, or empty for neither). `/history` in a
room posts both again.

In a terminal that runs no agent, what you type gets one reply: its screen a moment
later, without blank lines.

A group belongs to its herdr-app tab (by the tab's id): when the tab's terminal is
replaced, the new terminal carries on in the same group. A message that cannot be
delivered (the terminal has ended) says so plainly.

## Commands

- Direct chat: `/ls` (the overview again at the bottom of the chat; the old one is
  recalled), `/new` (a card asks: the folder to work in — under your home, relative —
  the agent — and, when several sessions are followed, the session —; one form, one
  submit), `/new <folder> [command]` (start one right
  away; default `claude`, the folder's name names the tab, the space follows your
  most recent terminal or `HERDRCHAT_SPACE`), `/new <session> <space>/<tab>
  [command]` (name the place yourself; its folder comes from a sibling in that
  space), `/sessions` (the hc sessions on this machine, which the bot follows, and each
  core's state; `/sessions add|drop <name>` changes what it follows — saved, then the
  bot starts again to apply; a session with live terminals is not dropped, and no
  core is created from chat), `/kill` (a card lists the live terminals by their groups'
  names; pick one and it ends — `/kill <session> <address>` still works), `/clean` (clean up ended
  terminals' groups and stale screen files; it asks first), `/auth` (see below). Feishu can have a bot menu for `/ls`, help and `/auth` (set in the
  developer console:
  `skills/herdrchat/references/feishu.md`, "The bot menu").
- In a terminal's room: any text goes to it (marked once typed in, and again when the
  turn that took it is over); an image (alone or in a rich text message) is sent
  beside the terminal with `hc put`, on its machine, and its path is typed in with your
  text, so an agent like Claude Code opens it; `/screen`, `/answer <text>` (type into the
  dialog it waits on, as is), `/enter` (press enter), `/esc`, `/history` (the conversation so far, as a file and
  a folded post). Dialog cards have buttons.
- On the command line, `herdrchat hooks` says whether herdrchat's hook is in Claude Code's
  and Codex's settings; `herdrchat hooks install [claude|codex]` puts it in (it shows the
  files and asks first; `--yes` does not ask), `herdrchat hooks uninstall` takes it out.
  See "From the agent itself, when it can say".

`herdrchat setup` does the steps below by asking: it checks the token or the Feishu keys
at once, picks the sessions, starts the bot and pairs you as its owner with a 6-digit
code you send to the bot, then offers the service. The steps say what happens, and how
to do it by hand. When the owner is missing later, `herdrchat pair` shows a new code.

## Set up: Feishu

Feishu only: Lark (the international version) is not supported yet. Steps 1, 2, 5 and 6
are in the Feishu developer console (`https://open.feishu.cn/app`). The herdrchat skill
(`/herdrchat setup`) clicks them for you with the [Ego Lite](https://lite.ego.app/)
browser, or tells you what to click without it; or follow the steps here yourself.

1. Create a custom app (企业自建应用) and add the bot ability (机器人).
2. Permissions: import the scopes listed in `skills/herdrchat/references/feishu.md`,
   "Scopes this bot uses" (there as JSON for the batch import): sending as the bot,
   reading the messages sent to it, making, renaming and deleting groups, reactions,
   images both ways, and group tags.
3. `herdrchat init` prints where its `config.env` is. Fill in the app's App ID and App
   Secret (the app's 凭证与基础信息 page). `herdrchat setup` also asks for the optional
   Encrypt Key and Verification Token (事件与回调 -> 加密策略); Enter skips them.
   `HERDR_SESSIONS` can stay empty for hc's
   default session (`ls ~/.local/state/herdr-core` lists yours).
4. `herdrchat start`. The bot must be running for the next step: the console checks for
   a live connection.
5. Events and callbacks (事件与回调): for events, choose the long connection mode (长连接)
   and add `im.message.receive_v1`; for callbacks (回调配置), choose the long connection
   mode too and add `card.action.trigger` (the dialog buttons).
6. Publish a version. If the page does not say 本次发布免审核, your team's admin must
   approve it first.
7. `herdrchat pair`: it shows a 6-digit code. In Feishu, search the app's name, open the
   bot and send it the code; the bot makes you its owner and starts again. One group per
   tab appears. When the code gets no answer, step 5 or 6 is not done.
8. Optional: send `/auth` to the bot so it can mute its groups for you ("Optional: let
   the bot act as you"), and add the bot menu (`skills/herdrchat/references/feishu.md`,
   "The bot menu").
9. Once it works: `herdrchat install` (see "Running as a service").

## Migrating from the Node version

The Go build takes over from the Node (npm) one in place: nothing is migrated, because
nothing changes. `config.env`, `state.json`, the asks folders and the hook
(`~/.local/share/herdrchat/hooks/ask.sh`, byte-identical, no need to reinstall
`herdrchat hooks`) are read as they lie. `herdrchat.ask` labels and the asks protocol
are the same, so a dialog asked while you switch is answered either way.

1. Stop the old one: `herdrchat stop` with the old command (it ran in an hc terminal
   and/or as a service; stop it in every form it has).
2. Put the new binary where the `herdrchat` command is (a release tarball, or
   `go install github.com/jrmarcco/herdr-chat/cmd/herdrchat@latest`); remove the npm
   package (`npm rm -g herdrchat`) so the old command is not the one found first.
3. `herdrchat start` — the same profile, groups, cursors and live cards continue
   (`herdrchat install` instead, if it ran as a service: the service file is rewritten
   to the new binary).

`herdrchat start` stops every old instance of the profile first (an hc terminal named
`herdrchat`, a running service), so running both at once does not happen by accident —
two bots would answer twice.

One difference: this build talks to Feishu only. A profile set to Telegram stays on the
Node version until Telegram is implemented here.

## Running as a service

`herdrchat start` runs the bot in an hc terminal, which ends when that hc core stops,
and nothing starts it after a reboot. `herdrchat install` makes it a service of the
system instead: a launchd agent on macOS, a systemd user unit on Linux. It starts at
login, comes back by itself after a crash, and does not depend on any hc core. It runs
the bot by its full path with only the variables it needs (hc's path, PATH,
the profile, the folders), so no agent's variables come along. With it installed,
`start`, `stop` and `status` go through the service; `herdrchat uninstall` removes it.
Each profile is its own service (`herdrchat install --profile <name>`).

- After moving or replacing the binary by hand, run `herdrchat install` again;
  `herdrchat status` says when the service's binary is gone.
- On Linux, `loginctl enable-linger` keeps it running after you log out.
- What the process prints before its log is open (a crash at start) goes to
  `logs/service.out.log` in the state folder.

## Optional: let the bot act as you

Some chat-list settings only you can change. Send `/auth` in the direct chat and allow it
on Feishu's page (OAuth device flow: a link and a code, no public address needed); the
token is refreshed on its own and kept in `user-token.json` in the state folder (mode 600).
Then:

- groups are muted (`HERDR_MUTED_SPACES` in `config.env`: `all`, the default; empty for none; or
  some spaces, comma-separated; a change applies both ways at the next start), and in a
  muted group a one-line reply that @-mentions you goes under each card that needs you and
  under each turn's last answer, unless its space is in `HERDR_SILENT_SPACES` (empty by
  default; same values) to stay fully quiet: in a muted group only a mention rings (a
  mention inside a card does not count as one), so the phone rings only then;
- each group is also in one of your own labels (the session or machine; `ended` once
  the terminal is gone), to filter the chat list by; the bot tags stay, since only they
  show next to a group's name. Either can be turned off: `HERDR_USER_TAGS=off` takes the
  groups out of your labels (the labels stay), `HERDR_BOT_TAGS=off` takes the bot's tags
  off (they are tenant tags, visible to the whole tenant);
- the group that needs you is pinned until it no longer does.

The app needs these user scopes: `im:chat.user_setting:write`, `im:feed_group_v1:read`,
`im:feed_group_v1:write`, `im:feed.shortcut:write`, `offline_access`. Without `/auth`
none of this happens and everything else works the same. If the token can no longer be
refreshed, the bot says so once and falls back.

## Skills

`skills/herdrchat/` is one skill for an agent (Claude Code or another that reads
`SKILL.md`): `/herdrchat setup`, `status`, `start`, `stop`, `install`, `logs`, `auth`,
`config`, `fix` and `feishu <task>` (the developer console, with Ego Lite from
https://lite.ego.app/ or as a guided walk-through). Details per part are in
`skills/herdrchat/references/`.
The skill lives in this repo's `skills/herdrchat/`; link it for your agent, for
example `ln -s "$PWD/skills/herdrchat" ~/.claude/skills/herdrchat`.

## Logs

Everything the bot logs goes to `logs/<YYYY-MM-DD>.log` in the state folder, one file a
day by your local date (the last 14 kept), as `<time> <LEVEL> <text>` with the time in
UTC. `herdrchat logs` shows today's (`--errors` only what went wrong, `--day 2026-01-31`
another day); a retry that recovered is a `WARN`, and a crash leaves its stack there
before it is started again. Tokens and secrets are never logged. The skill's
`references/ops.md` explains the lines.

## Limits

- Narration blocks are a condensed version of the text Claude showed, not the exact words.
- Claude Code and Codex have readers so far. pi and the rest post nothing by themselves
  until each has a reader; what you type to them gets their screen in reply.
- Codex: its log is found as the one written in its folder since it started (Codex names
  no pid outside its database), so two Codex sessions started in the same folder at the
  same moment could be told apart wrongly. Its questions with choices come as text: answer
  by typing. A group is named after Codex's thread name once it has one.
- Only text, images and rich text are passed on; files, stickers and the rest are not.
- An image that fails to upload three times is not sent; the group says so and why.
- Feishu only, not Lark (the international version) yet: the bot talks to open.feishu.cn.
- Feishu takes tag names of at most 16 characters; a longer session or machine name is cut
  at its last `-`, `_` or `.` that fits (`build-agent-server` becomes `build-agent`).
- Avatars are drawn with `sips`, so only on macOS; elsewhere groups keep Feishu's default.
- Without the agent naming its dialog (its API, or the hook), dialog buttons come from
  the screen's numbered menu; a dialog drawn some other way gets the screen and
  `/answer`, `/enter`, `/esc`.
- The hook (`herdrchat hooks install`) goes in on this machine only; on a connected
  machine, install herdrchat there and run it there too.

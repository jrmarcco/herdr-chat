# herdrchat-Go 重写规格书（spec.md）

> 本文档是 **Go 语言重写 herdrchat** 的完整实现规格，面向执行重写的 AI agent / 工程师。
> 目标：功能与原 Node.js 版（v1.10.2）完全对等，保持数据与协议兼容（可原地升级），并在架构上修正原版已知缺陷（见同目录 `optimization.md`）。
> 原则：**自包含**。本文复述了全部关键行为；源仓库仅作参考（`docs/guide.md`、`docs/architecture.md`）。

---

## 0. 术语表（全文档统一使用）

| 词 | 含义 |
|---|---|
| **hc / herdr-core** | 终端管理器，herdrchat 的地基。提供 `hc` CLI（events/get/send/keys/put/label/exec/run/kill/read/ls…） |
| **herdr-app** | herdr 的 GUI 客户端，把终端按 space/tab 组织，给终端打 `where`/`herdr.space`/`herdr.tab` 标签 |
| **session** | 一个 hc core 及其终端（`HC_SESSION`，默认 `default`） |
| **space / tab** | herdr-app 的侧栏组织方式；终端记录 `labels.where = "<space> / <tab>"` |
| **room** | 一个 tab 的镜像：飞书=一个群（chat_id），Telegram=bot 私聊中一个 topic（thread id） |
| **direct chat** | 与 bot 的私聊：总览与命令 |
| **profile** | 一套 herdrchat 实例（一 profile 一聊天 App），默认 `default` |
| **reader** | 读取某 agent 自身对话记录的模块（Claude Code / Codex / OpenCode） |
| **terminal key（k）** | 终端在状态中的键：`<session>|tN`（本机）或 `@<machine>|<machine>/tN`（远端机器） |
| **turn** | agent 从开始工作到离开 working 状态的一轮（hc `turn` 事件） |
| **dialog** | agent 等待人回答的界面（权限/提问/计划/表单） |
| **item** | reader 产出的对话条目：you / ai / tool / image / meta |

---

## 1. 项目目标与范围

重写一个 Go 程序 `herdrchat`（单二进制，macOS + Linux），实现：

1. 跟随 `HERDR_SESSIONS` 列出的每个 hc session 的事件流；
2. 为每个 herdr-app tab 维护一个 room（飞书群 / Telegram topic）；
3. 把 agent 的对话实时镜像到 room（活卡片 + turn 终答）；
4. 把 agent 的提问（hook / API / 屏幕三条通路）变成可点按钮的卡片，点击回写；
5. 用户在 room 打字/发图 → 注入终端；
6. 直聊总览 + `/ls` `/new` `/kill` 命令；
7. 完整 CLI（setup/pair/init/start/stop/status/logs/install/uninstall/hooks/run/version）；
8. launchd/systemd 服务管理；
9. PermissionRequest hook 安装器（写 Claude Code / Codex 设置文件）。

**兼容性承诺（硬性要求）**：
- `config.env`、`state.json`、`~/.local/state/herdrchat/asks/` hook 协议、`herdrchat.ask` 标签、`~/.local/share/herdrchat/hooks/ask.sh` 路径与内容 —— **全部不变**；
- 已有 Node 版用户停止旧进程、放入新二进制、`herdrchat start` 即完成迁移，状态无损接管；
- CLI 参数与输出语义保持一致（可补充，不可改变）。

**非目标**：Lark（国际版飞书）、Slack/Discord（架构预留）、herdr-app 本体。

---

## 2. Go 项目结构

```
herdrchat/
├── cmd/herdrchat/main.go          # 入口：分发子命令
├── internal/cli/                   # 每个子命令一个文件（setup.go/pair.go/start.go…）
├── internal/config/                # placesOf/loadConfig/parseEnv/setConfigValue
├── internal/hccli/                 # hc CLI 封装 + events 跟随器 + coreViews 拓扑
├── internal/state/                 # state.json 读写（原子写）+ schema 类型
├── internal/logging/               # slog 封装：按天文件 + 14 天保留 + crash capture
├── internal/core/
│   ├── engine.go                   # 编排：start/stop/事件接线（薄）
│   ├── lanes.go                    # per-key 串行车道
│   ├── rooms.go                    # 房间生命周期（ensure/adopt/end/revive/endGone）
│   ├── posting.go                  # 发帖管线（deliver/postItems/活卡片/图片上传）
│   ├── dialogs.go                  # 对话框状态机（postBlocked/onAction/claim）
│   ├── intake.go                   # 来自聊天侧的输入（typeInto/hold/imagesInto）
│   ├── overview.go                 # 总览（去抖 2s）
│   ├── cursor.go                   # 游标算法
│   ├── menu.go                     # 屏幕菜单解析
│   ├── meter.go                    # 页脚数据缓存（lastKnown/knownSoon）
│   ├── footer.go                   # footerWords/branchOf/tokens/windowName
│   ├── asks.go                     # hook 请求协议（文件 + 标签）
│   ├── pairing.go                  # 6 位配对码
│   └── service.go                  # launchd/systemd 生成与控制
├── internal/readers/
│   ├── reader.go                   # 契约接口 + 注册表 + checkReader + agentOf
│   ├── claude/                     # claude.go（transcript 解析）+ meter.go + ask.go
│   ├── codex/                      # codex.go + questions.go + ask.go
│   └── opencode/                   # opencode.go（API 封装）
├── internal/chats/
│   ├── chat.go                     # Chat 契约接口 + checkChat
│   ├── feishu/                     # client.go/cards.go/rooms.go/userauth.go/overview.go/avatar.go
│   └── telegram/                   # client.go(限速队列)/format.go(md→HTML)/rooms.go
├── skills/                         # 原样复用（见 reuse.md）
├── docs/                           # 原样复用 + 更新
└── testdata/                       # 从原 JS 测试提取的 golden 日志样本
```

**依赖政策**：运行时依赖尽量少。建议仅：
- `github.com/larksuite/oapi-sdk-go/v3`（飞书官方 Go SDK，含 larkws 长连接）；
- 其余全用标准库（Telegram 用 `net/http` 手写、CLI 用 `flag`、日志用 `log/slog`、JSON 用 `encoding/json`）。

---

## 3. 契约接口（Go）

### 3.1 Chat（聊天 App 契约，对应原 `lib/chats/contract.mjs`）

接口较大，允许拆成小接口组合，但**成员语义一一对应**，启动时 `checkChat` 反射校验齐全：

```go
type Chat interface {
    Name() string                                   // "feishu" | "telegram"
    Ready() bool                                    // owner 已知；未知前不建任何 room
    Help() []string
    RoomWord() string                               // "group" | "topic"
    LiveBytes() int                                 // 一张可编辑卡片容纳的字节数
    FoldedBytes() int
    Split(markdown string) []string                 // 按平台限制切分（代码围栏感知）
    Start(h Handlers) error
    Command(cmd string, args []string, where Where) (bool, error) // 平台自有直聊命令
    // rooms
    OpenRoom(look RoomLook) (Room, error)
    RenameRoom(room Room, name string) error
    PlaceRoom(k Key, place Place) error             // space 归属装饰（头像/标签/静音）
    EndRoom(k) error; ReviveRoom(k) error
    // posting
    Post(room Room, markdown string, footer string) (Message, error)
    Reply(where Where, markdown string) (Message, error)
    PostCard(room Room, look Look) (Message, error)
    EditCard(msg Message, look Look) error
    PostDialog(room Room, d Dialog, v DialogValue) (Message, error)
    CloseDialog(msg Message, d Dialog, done string) error
    // attention
    Pin(k Key, room Room, msg Message) error
    Unpin(k Key) error
    Pinned() []Key
    Ring(k Key, msg Message, text string) error     // 让手机响（若该 room 默认静默）
    MarkWorking(msg Message) (token string, err error)
    MarkDone(msg Message, token string) error
    // media
    Image(buf []byte) (markdown string, err error)
    PostFile(room Room, f FilePost) (Message, error)
    PostFolded(room Room, f FoldedPost) (Message, error)
    Download(msg Message, ref string, file string) error
    // owner
    TellOwner(look Look) error
    ShowOverview(rows []OverviewRow, opts OverviewOpts) error
}

type Handlers struct {
    OnMessage func(Incoming)                      // {Room(nil=直聊), Where, Message, Text, Images[]}
    OnAction  func(Click) (ActionResult, error)   // {Value, Message, Answer, Ticks} -> {Toast, Card?}
    OnReady   func()
}
```

`Look = {Title, Tone(working|closed|needs|done|good|bad), Markdown, Footer}`。
`Dialog = {Menu *Menu, Title, Markdown, DoneTitle, Footer}`；`Menu.own` 字段承载 agent 自报对话框原始引用。

### 3.2 Reader（agent 契约，对应 `lib/core/readers/index.mjs`）

```go
type Reader interface {
    App() string            // hc status.app，如 "claude-code"
    Processes() []string    // 前台进程名匹配集，如 ["claude"]
    After(session, address string, cur Cursor) (*Reading, error) // nil=该终端不可读
    Rest(session, address string, cur Cursor) ([]Item, error)    // 进程消失后 cursor.File 的剩余
}
// 可选能力接口：
type Dialoger    interface { Dialog(session, addr string) (*Dialog, error) } // ErrNoHook→读屏
type Answerer    interface { Answer(session, addr string, shown Dialog, r Reply) (bool, error) }
type Titled      interface { TitleOf(title string) string }
type Metered     interface { Meter(session, addr string, meta Meta) (MeterInfo, error) }
type StrayAsker  interface { StrayAsk(session, addr string) (id string, err error) }
type IdleAsker   interface { AsksIdle() bool }
```

`Reading = {Items []Item, File string}`。`Item`：

```go
type Item struct {
    Role string    // "you"|"ai"|"tool"|"image"|"meta"
    Text string
    At   time.Time
    ID   string    // 稳定 id（游标去重用）
    End  bool      // turn 的最后一条回答
    From string    // image: "you"|"ai"
    Media, DataB64 string
    Key, Err string            // 图片上传结果（chat.Image 的返回/失败说明）
    Model, Provider string
    Context *ContextUse        // {Used, Size}
    Limits  []Limit            // {Name, Used}
}
```

`agentOf(record)`：`status.app` 命中 reader **且**（前台进程名或 argv[0] basename ∈ Processes）；前台未知时只看 status.app（宁可多报不可哑终端）。

### 3.3 HC 与 Store（注入，测试可替换）

```go
type HC interface {
    FollowEvents(session string, h EventHandlers) *EventView
    Cores(session string) (CoreInfo, error)        // {Self, Links[{Name,ID,State}]}
    Machines(session string) ([]Machine, error)
    GetTerminal(session, address string) (*Terminal, error)
    ScreenTail(session, address string, lines int) (string, error)
    Send(session, address, text string, force bool) error
    Keys(session, address string, keys []string) error
    Put(session, address, localFile string) (remotePath string, err error)
    Run(session string, o RunOpts) (json.RawMessage, error)
    Kill(session, address string) error
    Label(session, address string, labels map[string]string) error
    NameTab(tabID, name string) error              // herdr-app CLI，可空实现
}
type Store interface { Read() State; Write(State) error }  // 原子写
```

---

## 4. 配置、状态与目录

### 4.1 目录布局（不变）

- 配置：`~/.config/herdrchat/<profile>/config.env`（`XDG_CONFIG_HOME` 可移）
- 状态：`~/.local/state/herdrchat/<profile>/`（`XDG_STATE_HOME` 可移）：`state.json`、`logs/<YYYY-MM-DD>.log`、`user-token.json`（飞书 /auth，0600）、`service.out.log`
- `HERDRCHAT_HOME=<dir>`：config 与 `state/` 同置一目录；**目录被移动时** profile id 加 `-<sha1 前 6>` 后缀（服务名 / hc 终端名随之），试验实例永不干扰正式实例
- profile 名规则 `^[A-Za-z0-9][A-Za-z0-9_-]*$`
- hook：`~/.local/share/herdrchat/hooks/ask.sh`；asks：`~/.local/state/herdrchat/asks/{req,ans}/`
- 目录权限 0700，敏感文件 0600

### 4.2 config.env 键（全表，格式：`KEY=value`，`#` 注释，环境变量优先）

| 键 | 平台 | 默认 | 说明 |
|---|---|---|---|
| `HERDRCHAT_CHAT` | both | `feishu` | `feishu` / `telegram` |
| `HERDR_SESSIONS` | both | `default` | 逗号分隔；空=仅 hc 默认 session |
| `HERDR_MUTED_SPACES` | both | `all` | 静默 space：`all`/空/逗号列表 |
| `HERDR_SILENT_SPACES` | both | 空 | 完全不响（连 needs you 也不响） |
| `HERDR_HISTORY` | both | `file,folded` | 新房间的历史回填形式 |
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | feishu | 必填 | 应用凭证 |
| `FEISHU_OWNER_OPEN_ID` | feishu | 空 | owner（配对时自动写入） |
| `HERDR_BOT_TAGS` / `HERDR_USER_TAGS` | feishu | `on` | bot 租户标签 / 用户标签开关 |
| `TELEGRAM_BOT_TOKEN` | telegram | 必填 | |
| `TELEGRAM_OWNER_ID` | telegram | 空 | owner |

`setConfigValue`：**逐行替换目标键、保留其余行与注释**；新键追加到末尾；写后 chmod 0600。

### 4.3 state.json schema（字段名与 JSON 形状保持兼容）

```jsonc
{
  "chats":      { "<k>": { "chat": "<room id>", "name": "<space> / <tab>", "session": "...", "tab": "<herdr.tab>", "ended": false } },
  "cursors":    { "<k>": { "at": "<ISO>", "seen": [["<id>","<ISO>"],…], "reader": "claude-code", "file": "<log>", "meta": {…}, "ended": false, "live": { "message": "<id>", "text": "…", "footer": "…" } } },
  "openCards":  { "<k>": { "message": "<id>", "dialog": {…} } },
  "reacts":     { "<k>": [ { "message": "<id>", "reaction": "<token>", "at": 1731580000000 } ] },
  "waiting":    { "<k>": [ { "where": "…", "message": "…", "session": "…", "address": "…", "text": "…" } ] },
  "pinned":     { "<k>": "<room|message>" },
  "overview":   { "message": "<id>" },
  "buttons":    { "<id>": { "value": {…}, "labels": {"1": "Yes"} } },   // telegram
  "spaces":     { "<space>": { "icon": 7322096 } },                     // telegram
  "feishuRead": { "<chat_id>": 1731580000000 },
  "telegramOffset": 12345,
  "down":       { "since": 1731580000000, "said": 1731580000000, "why": "…" },
  "pairing":    { "code": "123456", "until": 1731580900000, "tries": 0 }
}
```

写入必须原子（tmp+rename）。实现可加内存缓存，但**每次跨 await/跨 lane 操作后重新 Read**（多 lane 并发写的竞态靠"读-合-写"＋lane 串行化解，同原版语义）。

---

## 5. hc 集成层

### 5.1 CLI 封装（`internal/hccli`）

- `exec.CommandContext`，环境**剔除** `HC_TERMINAL_ID/HC_SCOPE/HC_TERMINAL_SOCKET`（bot 自身运行在 hc 终端里，不剔除则输入会被 hc 包成 `<hc from=…>` 跨终端笔记）；注入 `HC_SESSION=<session>`；`HERDRCHAT_HC` 环境变量可指定 hc 全路径（服务场景 PATH 不全）。
- 超时：默认 15s；`run` 30s；`send`/`put` 60s。输出上限 16MB。
- 关键命令映射：

| 用途 | 命令 |
|---|---|
| 事件流 | `hc events --json`（spawn 常驻，stdout 逐行 JSON） |
| 终端详情 | `hc get <addr> --json` |
| 输入 | `hc send [--force] <addr> -- <text>` |
| 按键 | `hc keys <addr> <key…>`（`1`/`esc`/`tab`/`up`/`down`/`enter`） |
| 传文件 | `hc put <addr> <file> --json` → `{path}` |
| 屏幕尾 | `hc read <addr> --lines <n>`（取最后 n 行） |
| 标签 | `hc label <addr> <k>=<v>…`（空值=删除） |
| 远端执行 | `hc exec --on <machine> sh -c '<script>'`（cwd=远端 home） |
| 新终端 | `hc run --bg --no-parent --json [--name n] --cwd <dir> [--label k=v]… -- <argv>` |
| 结束 | `hc kill <addr>` |
| 拓扑 | `hc server status --json`（core id）、`hc machine ls --json` |
| 本机 session 探测 | 读 `~/.local/state/herdr-core/<name>/core.json` 是否存在（**不得**用 hc 去问——会误建 session） |
| core 存活 | `core.lock` 首行 socket 路径，TCP 连接 1s 探测 |
| tab 命名 | `herdr-app ls` → 找 tab id → `herdr-app do tab.rename --id … --name …`（重试 10×1s） |

- 错误归一：`plainError(err)` 从 stderr 提取 hc 的 JSON `{"error":{message,kind}}`；`kind=="finished"` → "this terminal has ended."；`kind=="unavailable"` → 机器不可达（触发 machineDown 挂起）。

### 5.2 事件跟随器

- 每 session 一个 `hc events --json` 进程；**bufio.Scanner 缓冲区 ≥10MB**（终端快照行很长）。
- 协议：首行 snapshot `{seq, terminals}`；之后 JSON-RPC 通知 `event {seq,type,terminal,data}`、`heartbeat {seq}`、`missed {seq,terminals}`（missed→重放 snapshot）。`seq <= 已见` 丢弃。
- 事件类型→内存 Terminal 记录更新：`created/restarted`（set）、`removed`（delete）、`finished`（state=finished, foreground=nil）、`lost`（pid=nil, foreground=nil）、`status`（merge）、`labels`（record 或 labels）、`foreground`、`turn`（记 turn + 回调 onTurn{address,state,record}）、其余（title/size/keep→merge）。
- 进程退出：3s 后重启；stderr 写日志。
- **`turn` 事件仅在程序离开 working 时触发**；`state ∈ done|idle|error`（turn 结束）或 `blocked`（等人）。

### 5.3 多视图拓扑（coreViews）

一个终端可被多个 session 看到（本 session + 连到其 core 的机器）。终端**身份** = `<core id>|<tN>`：
- session 自身 core：`hc server status` 的 `core.id`；机器上的 core：`hc machine ls` 中 link 的 `id`；
- 跟随者选择：终端所在 core 的**直接 session** 优先；否则第一个（配置顺序）有 connected link 的 session；
- `owns()` 三值：true/false/null（拓扑未知，回退：本机地址→true；机器地址→第一个看到它 live 的 session）；
- `reachable()`：机器 link 非 connected → false（挂起该终端，不判死）；
- cores 每 60s 重读（机器增减）；机器 down/up 各只提示一次。

---

## 6. 核心引擎行为规范

### 6.1 车道（lane）

- 每终端 key 一条**串行 FIFO** 车道：同终端的房间操作/发帖/卡片互斥保序；不同终端并行；
- 全局门槛：所有 session 的**首个 snapshot** + 首次 cores 读取完成前，不派发任何 lane；
- 车道错误捕获：`unavailable` + 机器地址 → machineDown；否则 ERROR 日志（含 label 与地址）；
- Go 实现：per-key `chan func()` + 常驻 goroutine（空闲可回收），或 mutex+条件变量；语义必须等价。

### 6.2 房间生命周期

- **触发**：snapshot / `created|restarted|labels|status|title` 事件且终端 `labels.where` 存在 → `ensureRoom`；
- **命名**：`"<space> / <tab>"`；tab 是纯数字时 → 终端标题（reader 的 `TitleOf` 先清洗，如 Codex 去掉 spinner 前缀与 `Action Required`、取 `|` 前段；OpenCode 去 `OC | ` 前缀）→ 去首部非字母数字符号、压缩空白、截 40 字（`…`）→ 兜底 hc name；
- **归属 tab 而非终端**：建房间前若同 scope 内另一 key 有相同 `herdr.tab`：
  - 旧终端仍 live → 返回 WAIT（不建）；
  - 否则等旧终端 lane 排空（其 postEnd 会发遗言），把 state 中旧 key 的房间（连同 pinned/reacts 映射）**改挂到新 key**，房间内发 `↪ This tab runs a new terminal now (tN); it carries on here.`；
- 房间已存在：ended → revive；名字/session/tab 变化 → rename + 更新 state；每次都 `placeRoom`（幂等装饰）；
- 新房间：`openRoom` → 写 state → `placeRoom` → **历史回填**（见 6.7）；
- **endRoom**：终端结束 → 标记 ended + `chat.endRoom`（飞书 `ended` 标签 / Telegram 🔚 前缀）；revive 反向；
- **endGone**（启动后 & 机器连通时）：所有 session 中无任何 live 视图（按 identity 去重）的房间判 ended；远端机器未连通时**不判**。

### 6.3 发帖管线

- `deliver`：最多 5 次尝试（失败退避 1s,2s,4s,8s），每次先 ensureRoom；**最终失败则抛出且游标不推进**（内容随下次重发——宁可重复不可丢失）；
- `postItems(items, {live})`：
  1. `answerAt` = 首个 `End=true` 的下标；live 模式只取其前的条目（终答留给 turn 结束）；
  2. 图片预上传 `uploadImages`：每图按 item id 只传一次（内存 LRU 500）；同一图 3 次失败 → 标 `Err`，正文显示"未发送及原因"，不阻塞后续；
  3. 汇总 meta（model/provider/context/limits，新条目覆盖游标存量）；生成 footer（见 6.6）；
  4. `formatItems`：`tool` 行连续合并；`image` → markdown 图（你发的显示 **You:** 前缀）；`you` → 若与 fromChat 近 20 条文本匹配则**跳过**（你发的房间里已有），否则 `youOrOthers`：剥离 `<hc from="t7 review" reach="…">…</hc>` 包裹段显示为 **From t7 review** 引用块，剩余为 **You:** 引用块；
  5. live 模式 → 追加进活卡片；非 live → 头部（终答前）进活卡片后关闭（标题 `✅ Worked`），尾部（终答起）作新消息 + `ring("✅ done")`；
  6. 保存游标 `{at,seen,reader,file,meta,ended,live}`。
- **活卡片**：单条可编辑消息（标题 `⌛️ Working…`）；追加后超 `LiveBytes` → 当前卡关为 `(continued below)`、开新卡；编辑失败 → 整卡内容重发为新卡（ERROR 日志）；卡片引用存游标 `live`，重启后继续编辑同一张。
- **postTurn**（turn 结束）：有 reader → `postRead(waitForEnd=true)`（最多 20 次 × 750ms 等 `End` 条目；已见 ended 且无新条目则提前出）；成功且自用户消息后有新内容 → 完毕；**若用户消息后 records 一片空白**（unanswered 集合）→ 发屏幕（150 行）兜底；reader 抛错 → 屏幕兜底。
- **postEnd**（终端结束）：先 `reader.rest` 发完遗言 → `🔚 **Terminal ended** (reason)` → markDone 所有回执 → endRoom → 同 tab 的新 live 终端接管房间。
- 非 agent 终端（无 reader）：用户消息 → 等 1.5s → 回一屏 40 行屏幕。

### 6.4 游标算法（必须逐条移植）

```
LAG_MS = 10 分钟
isNew(item) = item.at > cursor.at && item.id ∉ cursor.seen
advance(cursor, items):
    seen = cursor.seen ∪ {(id,at)}
    newest = max(at)
    at' = max(cursor.at, newest - LAG_MS)     // 恒滞后 10 分钟
    seen' = seen 中 at > at' 者
```
- 日志行解析：跳过空行/坏 JSON（截断的首行、写一半的尾行）/无 timestamp 行；
- id 生成：有 uuid 用 uuid；否则 `sha1(type|timestamp|operation|content[|payload 特征])`；同 entry 多 item 加 `#i` 后缀（同毫秒不互撞）；
- 读法：先 tail 1500 行；若结果 `complete=false`（最旧可读行晚于 cursor.at）→ 整文件重读；
- **首启游标** = 启动时刻（`startCursor(startedAt)`）——**绝不补发历史**；换 transcript（`/clear`/resume）或进程消失时，先把 `cursor.file` 旧文件剩余发完。

### 6.5 对话框状态机（三条通路）

**总则**：
- 一个对话框只发一张卡：`postedDialog[key] = 对话框名`（有菜单=菜单哈希；无菜单=`kind|msg`）；离开 blocked 即遗忘（同题再问可再发）；
- 等待中的卡片登记 `openCards`（持久化）+ 内存 `dialogCards`（≤200，重启时从 openCards 恢复）；
- 对话框消失（状态离开 blocked / hook 标签消失 / idle 提问后重新工作）→ 卡片去按钮，显示结论（"✔ Answered." 等）；置顶随之撤销；
- 发卡片前先关旧卡（"It moved on; see the next card."）；
- `blocked` 到达瞬间**立即读屏**（此刻在终端里秒答的对话框也能留痕得卡）；屏幕读得太早（对话框未画完/正文数字列表被误当菜单）→ 需菜单带顶边框且两次读（间隔 400ms）同一哈希，或读满 5 次才采信。

**通路 A：hook / agent API（优先）**
- 触发：终端出现 `herdrchat.ask=<sec>-<pid>` 标签（labels 事件）→ `postBlocked(own=true)`；
- reader 的 `Dialog()` 返回（见 §7/§8/§9 各 agent 格式）→ 构造 `agentDialog`（选项=agent 原话；`menu.own` 留引用）→ 发卡；
- 点击 `onAction` → `answerOwn`：先 `Dialog()` 复核 key 相同 → 组 reply `{pick,text,ticks,esc}` → reader `Answer()` 回写 → 成功后若同一 form 还有下一问，原地刷新卡片为下一问；
- hook 标签消失：hook 已结束（聊天答的 / 终端答的 / 超时）→ 关卡；若屏幕仍 blocked（Codex 自己的 prompt）→ 读屏再发新卡，但**不覆盖**已发的屏读卡身份；
- Codex 后台服务模式：hook 在终端外跑、请求无 terminal 字段 → 引擎每 2s（本机）/每分钟（远端）扫 `strayAsk`（按会话 id 匹配 rollout 文件名）代打标签；标签的 hook pid 已死 → 撤标签。

**通路 B：OpenCode API**（reader 自带 Dialog/Answer，无 hook）。

**通路 C：屏幕读 menu（兜底）**
- `menuOf(screen)` 解析（算法见 §6.5.1）；
- 点击流程 `onAction`：
  1. 重读屏幕 + `hc get` 状态；状态非 blocked → "It no longer waits on this."；
  2. 菜单哈希 ≠ 卡片值 `k` → 不按键；**卡片原地刷新为当前屏幕的对话框**让用户重点（绝不另发新卡——新卡可能被 postedDialog 去重吞掉导致无人可答）；
  3. 选项 label 与卡片不一致 → "The options changed; nothing was pressed."；
  4. 按键：普通选项=数字；预览框问题（`beside`，无数字键）=从光标行到该选项的方向键 + enter；`Type something` = 先选中该项、300ms 后 `send --force` 文本；多选 Next = 每个与屏幕差异的勾选框按数字（间隔 120ms）+ tab；Esc = esc；
  5. 按键后 700ms 读下一状态：仍 blocked → 卡片刷新为下一步（同哈希如勾选切换 / 新对话框须 claim）；点击处理超 2s（慢机器）→ 先回 toast，后续新卡异步补发。

**6.5.1 菜单解析算法（移植自 menu.mjs，正则照抄）**

```
OPTION   = ^\s*(?:[❯›]\s*)?(\d{1,2})\.\s+(?:\[(.)\]\s+)?(.*\S)\s*$
         从 1 开始的连续编号段（最后一段为菜单）；行间允许描述行
CURSOR   = [❯›]
RULE     = ^[\s─╌━-]*$
BORDER   = ^\s*─{10,}        // 对话框顶边框
FREE     = ^Type something
BESIDE   = \s{3,}[┌└│├┤┐┘╭╰─┬┴┼║╔╚].*$   // 预览框：选项行右侧截断
LEFT_BORDER = ^(\s*)│\s?
MARKS    = [☐☑☒✔✓✗✘■□●○◉]   // 勾选标记（哈希时剔除）
top      = 选项区上方最近 BORDER；无则最近 Codex 条目行 ^•\s；再无则取选项上方 15 行（framed=false）
key      = sha1( top..last 行去 ❯›、去选项前导 [x]、去 MARKS 后以空格连接压缩空白 )[:12]
         // 空格连接抗 resize 重排
multi    = 存在 checked 选项（[x]/[ ]）
输出 Menu{key, options[{n,label,checked}], multi, free, text(边框下正文), framed, beside, cursor}
```

**消息暂存（hold）**：对话框等待期间用户发的文字，若无 free 输入框 → 存 `waiting[k]`（持久化），回复提示；对话框一离开 blocked → `sendWaiting` 按序注入；重启后对已不 blocked 的终端补发。

### 6.6 页脚（footer）

- 内容：`{harness 短名} · {model|screen} · {git branch} · context {pct}% ({tokens}) · {limit name} {pct}% · …`，未知项省略；
- `tokens()`：`<1e3` 原样；`<1e6` → `234k`；否则 `1.4M`；
- 分支：终端 cwd 上 `git branch --show-current`，空则 `git rev-parse --short HEAD`，每终端缓存 15s；
- meter 缓存 `lastKnown/knownSoon`：键=`{agent}|{machine}|window/{model}` 或 `|limits`；限额 TTL 5min、窗口 TTL 24h；首次最多同步等 10s，之后过期仅后台刷；
- `windowName(minutes)`：整除 1440→`N-day`，整除 60→`N-hour`，否则 `N-minute`。

### 6.7 历史回填（backfill）

- 新房间与 `/history`：读全量（cursor 起点 0000-00-00）→ 过滤 `at <= 房间游标.at` 且非 meta →
  - `file`：`<name>-<YYYY-MM-DD>.md`（首行元信息 + 全部条目；>20MB 截最旧、头部注明）；飞书≤30MB；
  - `folded`：按 `FoldedBytes` 从尾往前取**整条**，标题 `📜 Before this room (last N of M; all of it is in the file)`；
  - 静默发送，仅一次。

### 6.8 消息入口（来自聊天侧）

`onMessage`：
- 直聊（room=nil）：`/ls`（fresh 总览）、`/new <session> <space>/<tab> [cmd…]`（默认 `claude`；`--cwd <dir>` 前置；复用同 space 兄弟终端的 `herdr.space` id 与 cwd，没有则生成 `sp<rand>`/`tb<rand>`，labels 带 `where`；起 `/bin/zsh -lc "exec <cmd>"`；`nameTab` 命名）、`/kill <session> <addr>`、平台自有命令、否则帮助；
- 房间消息：房间→终端映射（state.chats 反查）；终端不 live → "Not delivered: this terminal has ended…"；图片先落 inbox（魔数判 png/jpg/gif/webp）→ `hc put` 到终端机器 → 删本地 → 路径拼进文本；
- `typeInto`：`/history`、`/screen`、`/esc`（优先 reader.Answer esc）、`/answer <text>`（`send --force`）；
  - 终端当前 blocked（以实时 `hc get` 为准）：有 free 框 → 直接作答；无 → hold；
  - 正常：`hc send`；记 fromChat（每终端近 20 条）、agent 终端记 unanswered；
  - `send` 报错含 blocked → 回屏幕 + `/answer` `/esc` 提示。

### 6.9 总览

- 行：`{name, address(跨 session 加前缀), state, kind, program, since(状态起始), room}`；
- 任意事件触发去抖 2s 更新；chat 侧原地编辑（内容相同跳过）；`/ls` 强制新发 + 回收旧的。

### 6.10 配对与 owner

- 无 owner 时任何直聊消息：非 `/` 开头 → `tryPairing`：码对 → 把 owner id 写 config.env → 回复成功 → 2s 后进程退出（由服务/终端循环拉起为新 owner 实例）；错 → 计数（5 次销毁配对）；无配对 → 回复指引（含自己的 id 如何手填）；
- 码：6 位、15 分钟、`crypto/rand`。

### 6.11 定时器与启停

| 定时器 | 周期 | 作用 |
|---|---|---|
| live | 5s | working 终端读增量入活卡片（每终端同时仅一次） |
| sweep | 60s | 空闲 readable 终端补读 + 补关回执（>30s 的）+ blocked 无卡补卡 + idle 提问补卡 + cores 刷新 |
| asks | 2s | 本机 asks 目录非空或有标签悬空时扫 stray/stale（目录 readdir，开销极小） |
| （sweep 内） | 60s | 远端机器的 claimAsks |

`start()`：日志起始行；cores 并行读；chat.Start；每 session FollowEvents；全部首快照后：endGone、补发 waiting、清 stale pins。`stop()`：清定时器，**优雅退出**（Go 新增：SIGTERM 触发，排空 lanes ≤5s）。

### 6.12 崩溃与恢复通知

- panic/uncaught → 日志（含栈）→ owner 卡片 `🔴 herdrchat is down`（原因≤300 字；10 分钟限频，防重启风暴）→ exit 1 由外层拉起；
- 起来后若 state.down 存在 → `✅ herdrchat is back after ~N min` + 原因，并清 down。

---

## 7. Reader 规格（逐 agent）

### 7.1 Claude Code（app=`claude-code`，processes=[`claude`]，footer 短名 `claude`）

**定位**：`hc get` 前台 pid → `~/.claude/sessions/<pid>.json` `{sessionId, cwd}` → transcript `~/.claude/projects/<cwd 的非字母数字全部替换为 '-'>/<sessionId>.jsonl`。远端机器经 `hc exec --on` 读同一相对路径。

**entry → items**（jsonl 每行一个 entry）：
- `isSidechain` → 跳过；
- `type=user && !isMeta && origin.kind=='human'`：文本 + 图片（顶层 image 块，from=you）→ you/image(you)；
- `type=user && !isMeta`：tool_result 内 image 块 → image(ai)；
- `toolUseResult.answers`（AskUserQuestion 结果）→ you，文本 `q → a` 每行；
- `type=attachment && attachment.type=='queued_command' && origin.kind=='human'` → you（mid-turn 注入）；
- `type=assistant`：逐 content 块：text → ai（`stop_reason=='end_turn'` 的 entry 标 End）；`thinking` 块且 base64(signature) 含 "narration" → ai（narration 是屏显文本的唯一落盘版）；`tool_use` → tool 行 `▸ {name}: {description|command|file_path|pattern|url|prompt 截 120}`；usage：`context.used = input+cache_read+cache_creation`；model 以 `<` 开头（synthetic）丢弃；纯 thinking 无输出但带 usage → meta 条目（只喂 footer）。

**dialog/answer（hook 通路）**：
- `AskUserQuestion`：多问题逐题卡（`key=<askId>#<i>`，未答题= partials 中没有的首个）；multiSelect → 卡片多选 + free"Type your own answer"；题干含 header/question/各 option 的 description/`question i of n`；答案聚合 `{question: answer}` 全齐后 `decision(allow, updatedInput={...tool_input, answers})`；
- `ExitPlanMode`：`key=<askId>`，三选项 Yes start / Yes and accept edits（附 `updatedPermissions setMode acceptEdits session`）/ No keep planning；正文=plan 截 3000 字 60 行；
- 其余工具授权：Yes /（`permission_suggestions` 非空时）Yes and don't ask again（回写 suggestions）/ No；正文=工具人类可读描述（Bash=`desc + $ cmd`；Write/Edit/MultiEdit=文件相对路径+diff；Read/Glob/Grep/WebFetch/WebSearch 各自格式；`mcp__srv__tool` → `srv: tool` + JSON）；
- Esc → `decision(deny, message="The user pressed Esc, in herdrchat (their chat).", interrupt=true)`（中断 Claude）；No → deny 带消息，不中断。

**meter**：`claude -p /context --model <m> --no-session-persistence` 解析 `Tokens: 13.8k / 1m` → 窗口；`claude -p /usage` 解析 `Current session: 2% used`（→5-hour）、`Current week (all models): 57% used`（→7-day）；均不产生模型调用。

### 7.2 Codex（app=`codex`，processes=[`codex`]，`asksIdle=true`）

**日志定位**（无 pid 线索，启发式）：`ps -o etime= -p <pid>` 得启动时刻 → `find ~/.codex/sessions -name 'rollout-*.jsonl' -mmin -<寿命+1min>`，逐文件首行取 `cwd` 与 `timestamp` → 过滤 cwd 匹配且 began≥started-5s，取最早；否则取文件名最大。**结果按 `<session>|<addr>|<pid>` 缓存**。

**entry → items**：`turn_context` → meta(model)；`event_msg/token_count` → meta（context={last_token_usage.total, model_context_window}，limits 由 rate_limits primary/secondary 的 `window_minutes`→windowName 排序）；`event_msg/item_completed`：UserMessage→you；AgentMessage→ai（`phase==final_answer && delivery!=async` 标 End；带 `questions` 的 → tool 行 `▸ asks you: …`）；CommandExecution→`▸ exec: <cmd>`（`-lc` 后即命令）；FileChange→`▸ edit: <type> <basename>…`；其余非 reasoning → `▸ <type>`。

**titleOf**：去首部 spinner 符号、去 `[ . ] Action Required | ` 前缀，取 `|` 前段。

**askDialog（hook）**：Yes / No / Answer in the terminal（空 decision 交还终端）；Esc=deny；文案 `requestWords`：Bash/apply_patch 展示 desc+command，MCP 工具 `srv: tool` + JSON。

**提问（无 hook，读日志）**：
- `request_user_input`（Plan 模式，等待中）：`response_item/function_call`，arguments JSON `{questions:[{id,header,question,options:[{label,description}]}]}`，直到同 `call_id` 的 `function_call_output` 或 `task_complete/turn_aborted`；卡片=**屏幕当前显示的那一题**（屏幕找 `Question i/n` 行与 `›` 光标选项号，且屏幕词包含该题题干前 40 字符才采信）；应答=按数字，或"None of the above"=方向键到该项+tab+`send --force` 文本；Esc=esc 键；
- `request_user_input_async`（不等待，agent 已 idle）：`item_completed` 的 AgentMessage.questions `[{title,options}]`；`idle=true` 卡片在 agent 非 working 期间常驻；多题逐题点选（内存 partial 聚合），齐了作为**一条消息**发出（多题格式 `title answer` 每行）；任何后续消息也算回答；Esc 只关卡。

### 7.3 OpenCode（app=`opencode`，processes=[`opencode`]）

- 一切经 `opencode api <operation> --param k=v [-d '<json>']`（在终端 cwd 执行；输出尾拼 `__herdrchat_exit=$?` 判断成功）；
- 会话选择：`session.list` 过滤 directory==cwd；`session.active` 命中的优先；否则 updated 最新；
- 消息：`session.message.list`（limit 100 游标翻页至 `since`，最多 50 页，逆序取回正序）；合成 JSONL 行喂游标：uuid=msg.id、timestamp=time.created；**End 判定**：其后首个"非 assistant 纯文本"消息 type==idle → 最后一条 ai 标 End；
- 条目：user→you；assistant 的 text 部件→ai、tool 部件→`▸ name: command|filePath|…`（question 部件特殊格式）；tokens.used=input+cache.read+cache.write；provider/model 喂 footer；
- meter：`model.list` 找 `modelID==model`（可加 provider 匹配）的 `limit.context`（24h TTL）；
- dialog：`session.form.list`（表单，field 逐题：type 含 multi/array 或 multiple → 多选；custom → free）+ `session.permission.list`（once/always/reject）；answer：form 逐 field 聚合（内存 partial）后 `session.form.reply {answer}`；Esc → `session.form.cancel` / permission reject；
- titleOf：去 `OC | ` 前缀。

---

## 8. hook 协议与安装器

### 8.1 ask.sh（**逐字节复用**原 `lib/hooks/ask.sh`）

协议要点：请求 `~/.local/state/herdrchat/asks/req/<epoch>-<pid>.json` `{"agent","terminal","at","request"}`；终端打标签 `herdrchat.ask=<id>`；答案 `ans/<id>.json`（内容=hook stdout 的 decision JSON，**空文件=无决策**）；24h 过期清理；Claude：blocked→非 blocked 即终端已答，grace 10s 内从未 blocked 也退出；Codex：一直等到答案/超时/终端结束。

### 8.2 asks 引擎侧（`internal/core/asks.go`）

- `askOf(record)`：标签值匹配 `^\d+-\d+$`；
- `openAsk`：标签 id → 远/本机 shell `kill -0 <pid> && cat req/<id>.json`；agent 匹配 + 有 request 才算；`finished` 内存集排除已答；
- `askOpen`（标签存活检查）、`strayAsks`（无 terminal 的请求列表，按 agent 过滤）；
- `answerAsk`：`decide()` 产出 `{decision}` / `{partial}` / null → partial 存内存（≤200）、decision 原子写 ans 文件（tmp+mv）；
- hook 是否在机器上：`[ -f ~/.local/share/herdrchat/hooks/ask.sh ]`，60s 缓存；不在 → `Dialog()` 返回哨兵"不可判"→ 直接读屏。

### 8.3 hooks 安装器

- Claude：`$CLAUDE_CONFIG_DIR|~/.claude/settings.json` 的 `hooks.PermissionRequest[]`；Codex：`$CODEX_HOME|~/.codex/hooks.json`；
- 条目：`{type:'command', command:"sh '<hook>' <agent> <wait>", timeout}`，wait：claude 1790s/codex 590s，timeout 1800/600；codex 加 statusMessage；
- 只增删命令含 `herdrchat/hooks/ask.sh` 标记的条目；首改自动备份 `<file>.herdrchat-backup`；解析失败的文件**绝不写**；原子写保持原 mode（新文件 0600）；
- CLI `hooks install [claude|codex] [--yes]`：非 TTY 且无 --yes → 拒绝；安装前打印将改的文件。

---

## 9. 聊天平台实现规格

### 9.1 飞书（`internal/chats/feishu`）

**连接**：官方 SDK larkws 长连接。事件：`im.message.receive_v1`（消息）、`card.action.trigger`（按钮，**3 秒内必须回 toast/card**）、`application.bot.menu_v6`（bot 菜单 → 视作直聊命令）。onConnect（首连+每次重连）触发补收。

**消息入**：按 message_id 去重（内存 ≤2000）；每 chat 一条处理队列（3s 时限，先登记再异步处理）；仅 owner（open_id）；messageParts 解析 text / image（image_key）/ post（富文本，抽文字+图）；@ 机器人的 `@_user_\d+` 剥离；其余类型回复"不支持"。**离线补收**：state `feishuRead[chat_id]` 水位 + 重连后 `im.v1.message.list`（start_time=水位秒，ByCreateTimeAsc，page 50）拉漏。

**卡片**（schema 2.0，`update_multi:true` 才可编辑）：
- markdown 卡 / 活卡片；header：tone→模板色+标准图标（working=blue/loading，needs=orange/warning，done|closed=grey/done，good=green，bad=red），标题去首 emoji；
- 对话框卡 `menuCard`：短选项（≤4 个且各 ≤14 字符）流式并排，长选项独行；多选用 form+checker（box_<n>，**无 behaviors 本地勾选不触发回调**）+ Next 提交（`ticksOf` 解析 form_value，**一个 box 都没有 → 拒答并提示**）；free 用 input+Send 提交（文本= form_value.answer）；Esc 红字按钮；按钮文本截 80，hover_tips 存全文；
- 关卡卡 `doneCard`：灰头、原文、结论小字；
- 折叠卡 `collapsible_panel`（被拒 → 降级普通帖）；
- footer：notation 小字，markdown 特殊字符 HTML 实体转义；
- chunk：6000 字切分，**代码围栏被切断时闭合并在下一段重开**。

**房间**：`im.v1.chat.create`（拉 owner 入群，name≤60）；重命名 chat.update；头像（macOS `sips` 画首字母色块 → im.v1.image.create type=avatar → chat.update，非 macOS 跳过）；**租户标签** `im.v2.tag.create`+`bizEntityTagRelation`（名字 >16 字符在最后一个 `-_. ` 处截断；全部标签调用走 250ms 串行队列防 5/s 限流）；ended = 标签 `ended`。

**以你行事（/auth，可选）**：OAuth2 **设备流**（userauth.go：startDeviceFlow → 链接+user_code → 轮询 token → 存 user-token.json 0600 自动刷新）；能力：mute 群（HERDR_MUTED_SPACES）、用户标签（feed_group）、置顶、卡片下 `replyMention`（`<at user_id>` 文本回复——**卡片内的 at 不算通知**，必须文本消息）；`allowed()` 每 60s 复查，通/断各处理一次（断时通知 owner 一次）；scopes：`im:chat.user_setting:write`、`im:feed_group_v1:read/write`、`im:feed.shortcut:write`、`offline_access`。

**总览**：单卡原地编辑（内容不变跳过；编辑失败重试 3 次退避后新发+撤旧 `message.delete`）。

**回执**：THINKING 表情 → 完成 OK（先删旧 reaction id 再加新）。

### 9.2 Telegram（`internal/chats/telegram`）

**连接**：纯 HTTP 长轮询 `getUpdates`（timeout 50s，allowed_updates=message|callback_query）；offset 逐条确认后持久化（state.telegramOffset）；409（他端同 token）→ 3s 重试；`getMe` 缓存 username + 检查 `has_topics_enabled`（关闭则 WARN 提示去 BotFather 开 Threaded Mode）；`setMyCommands` 注册命令表。

**限速**：**每 chat 一条 1.05s 间隔队列**（全局 ≈30/s 令牌桶总闸，Go 版优化点，原版为全局单队列）；429 按 `retry_after` 等待重试（≤4 次）。

**房间**：私聊内 topic（`createForumTopic`，name≤128，icon_color 按 space 从 6 色中取未用色并持久化到 state.spaces）；改名 editForumTopic；ended=名字加 🔚 前缀；**bot 无法建群/无法替你静音** → 静默发送（disable_notification）+ `ring` 发一条**非静默短回复**（reply_parameters 引用原消息）替代。

**消息**：仅 owner 且 chat.id==owner；`/cmd@botname` 剥后缀；图片取最大尺寸 file_id；bot 自己的服务消息（建 topic 等）跳过；callback_data 64 字节 → `<random id>:<pick>`，对话框值存 state.buttons（LRU 300，过期卡提示"too old"原地锁死）。

**排版**：markdown→HTML（format.go 移植：**bold**、`code`、```pre```、> 引用、链接、emoji 标题行），HTML 解析失败降级纯文本；4096 字符切分；活卡片上限 3500 字节；折叠=可展开 blockquote（不支持嵌套代码，纯文本转义）；图片随文本后逐张 sendPhoto（multipart，≤10MB）；文件 sendDocument ≤50MB；下载 getFile ≤20MB；回执 👀→👌（每消息一 reaction，替换式）。

**直聊命令**：`/` 触发命令列表（COMMANDS 常量表照抄）。

---

## 10. CLI 规格（`cmd/herdrchat`）

命令集与语义照抄原版（`setup/pair/init/start/stop/status/logs/install/uninstall/hooks/run/version`，`--profile/--chat/--day/-n/--errors/--yes`），差异与要点：

- `setup`（需 TTY）：①hc 在场检查（`--version`）；②选 chat（默认取已存配置）；③token/密钥即时校验（Telegram `getMe` + topic 模式引导重试；飞书 `tenant_access_token/internal`）——**密钥输入无回显**（raw mode 或 golang.org/x/term，Ctrl-C 130 退出）；④列 `~/.local/state/herdr-core` 选 sessions（不存在的报错重问；全 default 可留空）；⑤`start`；⑥飞书提示控制台配事件/回调/发布，Enter 确认；⑦`pair`；⑧询问装服务；
- `pair`：已设 owner → 提示；bot 未运行 → 失败提示；生成码 → 打印指引（Telegram 给 bot 链接）→ 每 2s 轮询 config 直到 owner 键出现（≤15min）→ `waitConnected`；
- `start`：readyConfig（必填键 + sessions 必须在本机）；**先 kill 旧实例**（所有 session 的 `hc kill/rm <terminal 名>`，防双 bot 双答）；有服务走服务（restart/load），否则在首个 session 起 `sh -c 'while true; do <bin>; echo …; sleep 3; done'` 的 `hc run --bg` 终端；随后 `waitConnected`（读当日日志等 `[feishu|telegram] connected`，60s，失败打印最近 5 条 ERROR 且 exitCode=1）；
- `install`：readyConfig → 停旧 → 写服务文件（**每次重写**；node 路径问题在 Go 版自然消失——直接写二进制绝对路径；env 白名单：HERDRCHAT_HC/HERDRCHAT_PROFILE/PATH/HERDRCHAT_HOME/XDG_*）→ load → waitConnected；launchd：Label `app.herdrchat.<id>`、KeepAlive、ThrottleInterval 10s；systemd：Restart=always、RestartSec=5、WantedBy=default.target，提示 enable-linger；
- `status`：profile/settings/state 路径；每 session core 是否 up；运行形态（服务 pid / hc 终端 / 双实例警告）；chat 必填键 set/missing（不打印秘密）；飞书 /auth token 状态（refreshUntil）；当日 ERROR/WARN 计数；
- `logs`：按天文件，`--errors` 过滤，`-n` 尾部；
- `hooks`：见 §8.3；
- 服务文件模板与 launchctl/systemctl 调用序列照抄原 `service.mjs`。

**全局**：EPIPE 友好退出；未知命令打印 usage exit 2；`run` = 前台直接跑引擎。

---

## 11. 日志

- `slog` 文本 handler；每行 `"<UTC ISO> <LEVEL> <text>"` 同时写 stdout 与 `logs/<本地日期>.log`（0600）；换行折叠为 ` ⏎ `（error 栈除外）；14 天保留；
- **capture**：接管标准 log 输出；三方库（SDK）的 `[error]` 行归 WARN；panic/uncaught → ERROR+栈 → onCrash（§6.12）≤3s → exit 1；
- **绝不记录** token/secret。

---

## 12. 并发与错误处理总则

1. lane 语义（§6.1）优先级最高：**同一终端的副作用全序、跨终端无锁干扰**；
2. 所有对 chat 平台的写操作可失败：发帖类失败走 deliver 重试；装饰类（rename/tag/avatar/pin）失败仅 ERROR 日志不重试；
3. `hc` 调用超时必须可取消（context）；事件进程唯一常驻，其余按需；
4. 内存缓存均有界（uploaded 500 / dialogCards 200 / partials 200 / buttons 300 / seen 2000）；
5. 状态写：读-合-写全量 JSON，原子 rename；lane 内无并发写同 key。

---

## 13. 测试与验收标准

**测试基建**（对应原 `test/fakes.mjs`）：
- FakeChat（记录全部调用）/ FakeHC（脚本化事件与终端）/ FakeReader / 内存 Store；
- `testdata/`：从原测试提取的 Claude transcript、Codex rollout、OpenCode API 响应 golden 样本；
- 集成：一个脚本化假 `hc` 可执行文件（Go test helper），按剧本回放 events 行。

**必须覆盖的行为测试**（原 `engine.test.mjs` 等的对等集）：
1. 契约校验：缺成员的 Chat/Reader 启动即报错；
2. 游标：同时间戳/晚写条目不漏、不重；tail 不足触发全量读；LAG 收缩；
3. 活卡片：满切卡、编辑失败重发、重启续编（state.live）；
4. 对话框：一题一卡；点击前屏幕已变 → 不按键且卡片刷新；选项文本变化 → 拒按；多选 Next 差异按键序列；hold→sendWaiting 顺序；answered-in-terminal 关卡；
5. hook：标签出现→卡；ans 写入→hook 拿到 decision；stray 认领；stale 标签撤除；
6. 房间：tab 接管（旧终端遗言先发）；endGone 不误杀断连机器；改名/复活；
7. 回填：file+folded 内容与截断；不重复；
8. 菜单解析：边框/无框/多选/free/beside 预览/resize 稳定哈希 各 golden 用例；
9. 三个 reader：golden 日志 → items 断言（含 End/meta/图片/部分聚合）；
10. 平台：飞书卡片 JSON 结构快照；Telegram md→HTML 与降级；两平台离线补收水位；
11. 服务文件生成（plist/unit 文本快照）；hooks 安装/卸载（含备份与坏 JSON 拒写）；
12. CLI：`--profile` 校验、usage、EPIPE。

**验收口径**：与原版并跑同一 FakeHC 剧本，聊天侧调用序列（FakeChat 记录）逐条一致；真实冒烟：`hc` + 飞书测试企业 / Telegram 测试 bot 走通 setup→pair→发消息→点按钮→服务安装 全链路。

---

## 14. 里程碑

| M | 内容 | 出口标准 |
|---|---|---|
| M1 | 骨架：config/state/hccli/logging/lanes + 契约 + Fake 测试基建 | 契约测试绿 |
| M2 | 引擎：房间/发帖/游标/活卡片/总览/入口（FakeChat 上） | §13.1-4,6,7 绿 |
| M3 | 对话框三通路 + hook 协议 + 菜单解析 + asks | §13.5,8 绿 |
| M4 | readers ×3（golden 全绿）+ meter/footer | §13.9 绿 |
| M5 | Telegram 实现（可先用真实 bot 冒烟） | §13.10 TG 部分 |
| M6 | 飞书实现（含 /auth 设备流、标签、补收） | §13.10 飞书部分 |
| M7 | CLI 全命令 + 服务 + hooks 安装器 + 日志 capture | §13.11,12 |
| M8 | 端到端验收 + 文档（README/guide/architecture 更新）+ 迁移说明 | 冒烟全链路 |

---

## 15. 常量速查表（从原版逐个抄录）

| 常量 | 值 |
|---|---|
| SHELL_ECHO_MS / SCREEN_LINES / TURN_SCREEN_LINES | 1500 / 40 / 150 |
| POST_TRIES（退避 1,2,4,8s）/ SWEEP_MS / SWEEP_DONE_MS | 5 / 60000 / 30000 |
| LIVE_MS / ASKS_MS / END_TRIES×END_WAIT_MS | 5000 / 2000 / 20×750 |
| KEY_SETTLE_MS / CLICK_BUDGET_MS / OVERVIEW_WAIT_MS | 700 / 2000 / 2000 |
| UPLOAD_TRIES / DRAW_TRIES / DRAW_WAIT_MS / BOX_GAP_MS | 3 / 5 / 400 / 120 |
| TITLE_MAX / DIALOG_LINES / FILE_BYTES | 40 / 15 / 20MB |
| BRANCH_MS / HOOK_CHECK_MS / LAG_MS / TAIL_LINES | 15s / 60s / 10min / 1500 |
| hook WAIT/TIMEOUT claude|codex | 1790/1800s | 590/600s |
| meter LIMITS_MS / WINDOW_MS / FIRST_WAIT_MS | 5min / 24h / 10s |
| TG: POLL/RETRY/SEND_GAP/TRIES/NAME_MAX/BUTTON_TEXT/BUTTONS_KEPT/liveBytes | 50s/3s/1050ms/4/128/60/300/3500 |
| 飞书: CHUNK/liveBytes/foldedBytes/OVERVIEW_TRIES/YOU_CHECK_MS/TAG_MAX/TAG_GAP_MS | 6000/20000/20000/3/60s/16/250 |
| 按钮 TRUNC：飞书 80 / TG 60；SIDE_BY_SIDE | ≤4 个且各 ≤14 字符 |
| 日志 KEEP_DAYS / CRASH_WAIT_MS / CRASH_NOTICE_MS | 14 / 3s / 10min |
| PAIR_MINUTES / PAIR_TRIES / CONNECT_WAIT_MS | 15 / 5 / 60s |
| events RESTART_MS / hc 超时 默认|run|send|put / maxBuffer | 3s / 15|30|60|60s / 16MB |
| Telegram 平台保留离线消息 24h；飞书文件 30MB、TG 图片 10MB / 文件 50MB / 下载 20MB | |

标题常量：`⚠️ Needs you` / `✅ Done` / `⌛️ Working…` / `⌛️ Working… (continued below)` / `✅ Worked`；TG 表情 👀/👌；飞书 THINKING/OK。

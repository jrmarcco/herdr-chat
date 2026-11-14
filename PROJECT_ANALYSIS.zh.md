# herdrchat 项目分析报告

> 分析对象：`herdrchat` v1.10.2（npm 包，MIT 协议，作者 zhangxiangliang）
> 分析范围：功能、架构、模块设计、并发模型、状态管理、可靠性、安全、测试与工程实践
> 代码规模：`bin/` + `lib/` 源码约 **6,500 行**（33 个文件），测试约 **2,500 行**（22 个文件）

---

## 1. 项目定位与解决的问题

**herdrchat 是 hc（herdr）的"聊天客户端"**。herdr 生态分为三层：

| 角色 | 是什么 |
|---|---|
| **hc / herdr-core** | 终端进程管理器：让 coding agent（Claude Code、Codex、pi 等）跑在常驻终端里，并提供事件流、输入注入等 API |
| **herdr-app** | 桌面/手机 GUI 客户端：把终端按 space/tab 组织展示 |
| **herdrchat**（本项目） | 聊天客户端：把每个 herdr-app 的 tab 镜像到飞书群或 Telegram 话题中 |

**解决的痛点**：herdr-app 是直连架构，离开办公网络就需要 VPN/内网穿透；手机小屏操作终端体验差。herdrchat 让机器人**主动外连**（飞书长连接 / Telegram 长轮询），无需公网 IP 和 VPN；用户在自己已有的聊天 App 里读 agent 输出、点按钮回答提问、收发图片、看全局总览。

**一句话概括**：把"远程看管一堆 AI 编程终端"变成"刷一个聊天软件"。

### 运行前提

- 本机运行 hc，至少一个 hc session；
- Node.js ≥ 20，macOS 或 Linux；
- 飞书（企业自建应用，暂不支持国际版 Lark）或 Telegram bot（topic 模式）。

---

## 2. 功能全景

### 2.1 房间（Room）模型 —— 每个 tab 一个聊天空间

| 概念 | 飞书 | Telegram |
|---|---|---|
| 一个 tab | 一个群，命名 `<space> / <tab>`，打 session/机器标签 | bot 私聊中的一个话题（topic） |
| 一个 space | 统一群头像（首字母 + 独立配色，macOS 用 `sips` 绘制） | 话题图标颜色（Telegram 六色轮换） |
| 终端结束 | 群保留历史，打 `ended` 标签 | 话题改名加 🔚 前缀 |
| 直聊（侧栏） | 总览卡片（原地编辑更新）+ `/ls` `/new` `/kill` + bot 菜单 | 主线程总览消息 + `/` 命令列表 |

- 房间**归属 tab（按 `herdr.tab` 标签 id）而非终端**：tab 里的终端被替换后，新终端接管原房间（`adoptTabRoom`），旧终端的"遗言"先发完；
- 无名字的 tab（纯数字）用终端标题（agent 自己设置的会话名/主题）兜底命名，超过 40 字截断。

### 2.2 会话镜像 —— 三层数据源叠加

这是本项目最核心的设计。hc 只能给出"屏幕"，而屏幕是图片：长输出被折叠、无法区分用户与 agent 的话。因此 herdrchat 分层取数：

1. **hc 事件流为骨架**（`hc events --json`）：终端的存在、状态、turn 结束时机；输入通过 `hc send` 注入；
2. **Reader 读取 agent 自己的记录**（对话内容）：
   - **Claude Code**：通过终端前台 pid → `~/.claude/sessions/<pid>.json` 定位会话 → 读 `~/.claude/projects/<cwd>/<sessionId>.jsonl` transcript；支持 narration 块（带 narration 签名的 thinking 块）、queued_command、AskUserQuestion 结果等特殊条目；
   - **Codex**：读 `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`；因 Codex 不记录 pid，用"进程启动时间（ps etime）+ cwd"启发式定位日志文件；解析 `item_completed`、`turn_context`（模型名）、`token_count`（上下文与限额）；
   - **OpenCode**：不读文件、不读屏，直接调 `opencode api`（session.list / session.message.list / session.form.list / session.permission.list），API 稳定即兼容；
   - 其他 agent：无 reader，不主动发言；用户发消息会收到一屏屏幕截图式的回复。
3. **屏幕读取作为兜底**（`menu.mjs`）：解析终端屏幕上的编号菜单对话框。

### 2.3 工作中的实时展示 —— "活卡片"

- 一个 turn 只有一张 `⌛️ Working…` 卡片，**每 5 秒编辑一次**（编辑不是新消息，手机不会每几秒响一次）；内容含 agent 的话、你中途插入的 prompt（**You:**）、每次工具调用一行（`▸ Bash: Run the tests`）；
- 卡片满 20 KB（Telegram 3.5 KB）就关闭为 `(continued below)` 并开新卡；编辑失败就整卡重发（**宁可重复、不可丢失**）；
- turn 结束：最后一条回答作为独立新消息发出（Claude 会等 transcript 里 `stop_reason: end_turn`，最多重试 20 次 × 750ms，因为 hc 报告结束早于 agent 落盘）。

### 2.4 对话框应答 —— 三条通路，优先级从高到低

1. **agent 自报**（最优，不读屏）：
   - **herdrchat 的 PermissionRequest hook**（`lib/hooks/ask.sh`，由 `herdrchat hooks install` 写入 Claude Code 的 `settings.json` 与 Codex 的 `hooks.json`，改动前自动备份）：
     - hook 把请求写到 `~/.local/state/herdrchat/asks/req/<秒>-<pid>.json`，给终端打 `herdrchat.ask=<id>` 标签；
     - herdrchat 通过 hc 标签事件感知，发卡片（agent 的原话和选项）；
     - 用户点击后把决策写入 `ans/<id>.json`，hook 读出并作为其输出（allow/deny/updatedInput/updatedPermissions）；
     - 覆盖：Claude 的工具授权（Yes / Yes 且不再询问 / No）、AskUserQuestion 多问题逐题卡片、ExitPlanMode 计划审批；Codex 的命令/补丁/MCP 工具（Yes / No / Answer in the terminal）；
     - Codex 走共享后台服务时 hook 在终端外运行、无法标终端 —— reader 的 `strayAsk` 按会话 id 匹配"流浪请求"并代为打标签（本机 2 秒轮询、远程机器 1 分钟）；
   - **OpenCode 自有 API**：`session.form.list` / `session.permission.list`，应答走 `session.form.reply` / `session.permission.reply`；
   - Codex 的提问（Plan 模式 `request_user_input` 与异步 `request_user_input_async`）从它自己的日志解析，无需 hook。
2. **屏幕读取兜底**（无 hook / agent 未自报）：
   - `menu.mjs` 解析屏幕编号菜单：识别边框、多选框、`Type something` 自由输入项、预览框（`beside` 模式下无数字键，用方向键 + Enter）；
   - **防陈旧卡片机制**：对话框以"内容哈希"（去掉光标 ❯、勾选标记、预览框后 sha1 取 12 位）为身份；点击前重读屏幕校验哈希与选项文本一致才按键，否则卡片自动刷新为新对话框让用户重点一次；
   - 多选题：卡片上即时勾选（本地状态），Next 一次性把差异项逐个按键再按 tab。
3. **可靠性细节**：一题一卡（`postedDialog` 声明式去重）；在终端里直接回答会自动关卡片；卡片等待期间用户发的文字进入"暂存队列"（`waiting`），对话框结束后依序注入，永不丢弃；`/answer` `/esc` `/screen` 命令直接操纵。

### 2.5 元信息页脚（meter）

每条消息下方一行小字：`claude · claude-opus-5-5 · main · context 23% (234k) · 5-hour 2% · 7-day 57%`

- harness 名来自 hc；模型名/上下文用量来自 agent 日志（各 reader 解析 usage/tokens）；git 分支通过机器上 shell 执行 `git branch --show-current`（15 秒缓存）；
- Claude 的上下文窗口与限额来自 `claude -p /context`、`claude -p /usage`（Claude Code 自答，**不产生模型调用**）；OpenCode 窗口来自 `model.list`；全部经 `meter.mjs` 的 `lastKnown/knownSoon` 缓存（限额 5 分钟、窗口 24 小时，首次最多等 10 秒，之后后台刷新不阻塞发帖）；
- 未知的字段直接省略。

### 2.6 通知治理（这是手机端可用性的关键）

| 机制 | 飞书 | Telegram |
|---|---|---|
| 免打扰 space（`HERDR_MUTED_SPACES`，默认 all） | 群静音（需 `/auth` 用户 token），仅 @ 提及会响 | 消息静默发送，仅"needs you/done"的短回复带声 |
| 完全静默 space（`HERDR_SILENT_SPACES`） | 连 @ 都不发 | 连响铃回复都不发 |
| 需要你时 | 群置顶 + 卡片下 @ 回复 | 对话框卡片置顶在话题内 |
| 消息回执 | 👀(THINKING) → 👌(OK) 表情回应 | 👀 → 👌 |

- 飞书 `/auth`：OAuth **设备授权流**（链接 + code，无需公网地址），token 存 `user-token.json`（mode 600，自动刷新），使 bot 能"以你的身份"执行静音、打用户标签、置顶三类只有用户能做的操作；不可用时降级且只提醒一次。

### 2.7 可靠性设计（"Nothing is lost"）

- **游标机制**（`cursor.mjs`）：`{at, seen}`，`at` 落后最新条目 10 分钟（`LAG_MS`），配合已见 id 列表 —— 时间戳相同或晚写的条目不会漏，也不会重发；
- **发帖重试**：5 次指数退避（2/4/8/16s），仍失败则游标不动，内容随下次一起发出（**重复可接受，丢失不可**）；
- **重启续传**：state.json 持久化每终端游标、打开的卡片（openCards）、消息回执（reacts）、暂存消息（waiting）、置顶、总览消息 id、Telegram 按钮 id 表、轮询 offset —— 重启后继续编辑同一张活卡片；
- **多重扫描**：空闲终端在启动时、每次快照后、每 60 秒 sweep 一次（补漏事件/晚落盘）；`hc events` 断线 3 秒自动重启；Telegram 离线消息由平台保留 24h + offset 断点续读；飞书自建 `feishuRead` 水位 + 重连后 `message.list` 补收；
- **崩溃通知**：uncaughtException → 写日志 → 给 owner 发"🔴 down"卡片（10 分钟限一次，防重启风暴刷屏）→ 退出由外层拉起；恢复后发"✅ back + 停机时长"；
- **Claude 换 transcript**（`/clear`、resume）：先把旧文件剩余内容发完再切新文件；终端结束：先读完日志遗言再宣布结束。

### 2.8 历史回填

新房间不空白：全部对话存为 `.md` 文件（>20MB 截断最旧部分）+ 最新一段折叠面板（飞书 collapsible_panel / Telegram 可展开引用），静默发送、只发一次；`/history` 可重发。`HERDR_HISTORY` 可配置 file/folded。

### 2.9 其他用户功能

- 群内直接打字 → `hc send` 注入终端（`force` 用于回答对话框）；图片：下载到本机 → `hc put` 送到终端所在机器 → 把路径连同文字一起输入（agent 如 Claude Code 会自动打开）；
- 跨终端消息识别：`hc send` 从别的终端发来的消息被 hc 包成 `<hc from="t7 review">` 标签，展示为 **From t7 review** 而非 **You**；
- 多机器：终端在远端机器时，读文件/执行命令走 `hc exec --on <machine>`；core 拓扑（`views.mjs`）按 core id 去重同一终端的多个视图，保证只有一个 session 跟随它；机器断连时挂起而非误判结束。

### 2.10 CLI 与运维

```
herdrchat setup | pair | init | start | stop | status | logs | install | uninstall | hooks | run | version
```

- `setup`：交互式向导，每步即时校验（Telegram `getMe` + topic 模式检查；飞书 `tenant_access_token` 校验），密钥输入无回显（手写 raw-mode 读取），选 session，启动，6 位配对码，询问是否装服务；
- `pair`：配对码 15 分钟有效、最多 5 次错误尝试（`pairing.mjs`）；发码同时验证了"bot 运行 → 平台送达 → 事件通路"整条链路；
- `install`：macOS 生成 launchd LaunchAgent（KeepAlive + 10s 节流），Linux 生成 systemd user unit（Restart=always + 5s）；**环境变量白名单**（仅 hc 路径、PATH、profile、目录），杜绝 agent 的变量泄漏进服务；node 升级换路径后 `status` 会提示重装；
- `start` 无服务时退化为 hc 终端内 `while true; do …; sleep 3; done` 守护循环；
- `hooks`：查看/安装/卸载 PermissionRequest hook（先展示将改动的文件并确认，`--yes` 跳过）；
- `status`/`logs`：运行状态（服务/终端/双实例检测）、配置完备性、当日错误计数、日志按天分文件保留 14 天，密钥永不入日志。

### 2.11 Skill（给 coding agent 的操作手册）

`skills/herdrchat/` 是一个可被 `npx skills add` 安装的技能包：教 Claude Code/Codex 等 agent 执行 `/herdrchat setup|status|fix|config|start|stop|install|logs|auth|feishu|telegram`，含 4 份参考文档（飞书控制台逐步操作、Telegram、运维排障、herdr 安装）。规则严谨：外发动作先征求同意、绝不打印密钥、只读检查先行。

---

## 3. 架构分析

### 3.1 总体结构：双插件 + 中立内核

```
                        ┌────────────────────────────────────────────┐
                        │                bin/cli.mjs                │
                        │   setup/pair/init/start/stop/status/logs/  │
                        │   install/uninstall/hooks/run (运维入口)    │
                        └────────────────────┬───────────────────────┘
                                             │ start
                                             ▼
                        ┌────────────────────────────────────────────┐
                        │            bin/herdrchat.mjs              │
                        │        组装: config + log + chat + engine  │
                        └───────┬────────────────────────┬───────────┘
                                │ contract (注入)         │ 注入
              ┌─────────────────▼──────────┐   ┌──────────▼──────────────────┐
              │      lib/chats/<app>/      │   │        lib/core/            │
              │  ┌──────────┐ ┌─────────┐  │   │  engine.mjs (1,479 行)      │
              │  │  feishu  │ │telegram │  │◄──┤  hc.mjs / events.mjs       │
              │  │ Lark SDK │ │ raw HTTPS│  │  │  views.mjs (多机拓扑)       │
              │  │ 长连接    │ │ 长轮询   │  │  │  cursor/menu/meter/asks    │
              │  └──────────┘ └─────────┘  │  │  config/log/service/pairing │
              │  contract.mjs 契约+校验     │  │  readers/ claude│codex│     │
              └────────────────────────────┘  │          opencode (插件)     │
                                              └─────────────────────────────┘
                                                         │ execFile/spawn
                                              ┌──────────▼──────────────────┐
                                              │   hc CLI (herdr-core)       │
                                              │  events/get/send/keys/put/  │
                                              │  label/exec --on/...        │
                                              └─────────────────────────────┘
```

**两条铁律**（`docs/architecture.md` 明文规定，代码中确实遵守）：

1. `lib/core/` **从不** import `lib/chats/`；chat app **从不** import engine；两者只在 `lib/chats/contract.mjs` 的契约上相遇；
2. core 的 hc、readers、store 全部以参数注入 —— 测试用 `test/fakes.mjs` 的假实现替换，不碰真实网络。

这是一种**端口-适配器（六边形）架构**的轻量实现：

- **chat 契约**（`CHAT_MEMBERS`，约 30 个成员）：`ready/start/onMessage/onAction`、`openRoom/renameRoom/placeRoom/endRoom`、`post/postCard/editCard/postDialog/closeDialog`、`pin/ring/markWorking/markDone`、`image/postFile/postFolded/download`、`tellOwner/showOverview`、`liveBytes/foldedBytes/split`… `checkChat()` 在启动时全量校验，缺一个成员立即报错而非运行到一半才炸；
- **reader 契约**（`READER_MEMBERS`）：必选 `app/processes/after/rest`，可选 `dialog/answer/titleOf/meter/strayAsk/asksIdle/short`；`checkReader()` 同样启动即校验；`agentOf()` 用 `status.app` 选 reader 并校验前台进程名（防止 hc 状态残留把已退出的 agent 误判为活跃）。

新增一个聊天 App = 新建文件夹 + 实现契约 + 注册 + 配置项 + 测试 + 文档（架构文档给出 7 步清单）；新增一个 agent = 写一个 reader 模块 + 登记 + 测试（5 步清单）。**Slack/Discord 与 pi 是文档明示的扩展方向。**

### 3.2 各模块职责与规模

| 模块 | 行数 | 职责 |
|---|---|---|
| `lib/core/engine.mjs` | 1,479 | 内核：房间生命周期、发帖管线、对话框状态机、lane 并发、总览、消息入口 |
| `bin/cli.mjs` | 627 | 全部 CLI 命令与交互式 setup |
| `lib/chats/telegram/index.mjs` | 470 | Telegram 契约实现 |
| `lib/core/readers/codex.mjs` | 396 | Codex reader（含日志定位、两类提问解析） |
| `lib/chats/feishu/client.mjs` | 377 | 飞书 API 封装（卡片、按钮、标签、事件长连接） |
| `lib/chats/feishu/index.mjs` | 348 | 飞书契约实现 |
| `lib/core/readers/claude.mjs` | 275 | Claude Code reader |
| `lib/core/readers/opencode.mjs` | 254 | OpenCode reader |
| `lib/core/hc.mjs` | 193 | hc CLI 薄封装 + 屏幕清理 + 会话探测 |
| `lib/core/config.mjs` | 168 | profile/目录/解析/写入 config.env |
| `lib/core/service.mjs` | 166 | launchd/systemd 生成与控制 |
| 其余 14 个模块 | 40–174 | asks/cursor/menu/meter/views/events/log/hooks/pairing/home/decor/groups/userauth/overview/avatar/format 等 |

依赖极克制：**运行时依赖仅 1 个**（飞书官方 SDK `@larksuiteoapi/node-sdk`）；Telegram 侧用原生 `fetch` 手写（零依赖），限速队列（1.05s 间隔）+ 429 `retry_after` 自适应。

### 3.3 并发模型：lane（车道）

`engine.mjs` 的核心并发原语是 **per-terminal 串行 lane**：

```js
const lanes = new Map(); // 终端 key -> 该终端排队的最后一个 Promise
function laneOf(k, label, fn) {
  const next = (lanes.get(k) || ready).then(fn).catch(...);
  lanes.set(k, next); ...
}
```

- 同一终端的所有工作（读日志、发帖、发卡片、关卡片）**串行保序**，杜绝对其房间与游标的竞态；
- **不同终端互不阻塞**；
- 启动门槛：所有 session 的首个事件快照 + cores 读取完成（`firstSnapshot` + `ready` Promise）之前不派发任何 lane —— 保证远端机器终端总由同一个 session 认领；
- 飞书侧重叠一层 per-chat 队列（3 秒回调时限），Telegram 侧全局发送队列（1 msg/s）。

### 3.4 事件驱动数据流（一次 turn 的生命周期）

```
hc events --json (每 session 一个 spawn, 断线 3s 重启)
   │ snapshot → 首次建房间
   │ status   → onStatus: blocked→发卡片; 离开 blocked→关卡片/解置顶/发暂存消息
   │ labels   → onAsk: herdrchat.ask 标签出现/消失 = hook 提问开始/结束
   │ turn     → onTurn: blocked→先发所说内容再发卡片; 结束→postTurn(等终答→发正文→markDone→ring)
   ▼
engine lane ──► reader.after(cursor) ──► cursor diff ──► uploadImages(缓存每图一次)
   ──► footer(branch+meter) ──► live card / 新消息 ──► saveCursor
```

四条定时器：`LIVE_MS` 5s（工作中终端的活卡片刷新）、`SWEEP_MS` 60s（空闲补漏 + 远机器 asks）、`ASKS_MS` 2s（本机 hook 请求扫描，仅目录 readdir，开销极小）、还有每 60s 的 cores 拓扑刷新。

### 3.5 状态管理

**配置**：`~/.config/herdrchat/<profile>/config.env`（`setConfigValue` 增量改写保留注释）；**状态**：`~/.local/state/herdrchat/<profile>/state.json`（原子写：tmp + rename）。state.json 关键字段：

| 字段 | 用途 |
|---|---|
| `chats` | 终端 key → 房间（id、名字、tab、ended 标记、session） |
| `cursors` | 每终端 reader 游标（含 `live` 活卡片消息 id 与文本，重启续编） |
| `openCards` / `postedDialog`（内存） | 等待中的对话框卡片 / 一题一卡去重 |
| `reacts` / `waiting` / `pinned` | 消息回执待完成 / 对话框期间暂存的消息 / 置顶记录 |
| `buttons`（TG）/ `spaces`（TG） | callback_data 64 字节限制的按钮 id→对话框映射（保留 300）/ space→图标色 |
| `feishuRead` / `telegramOffset` | 两平台的离线补收水位 |
| `overview` / `down` / `pairing` | 总览消息 id / 停机信息 / 配对码 |

**Profile 体系**：`HERDRCHAT_PROFILE` 或 `--profile`，一 profile 一聊天 App 并行运行；`HERDRCHAT_HOME` 可整体搬迁，目录被移动时自动给服务名/hc 终端名加短哈希后缀 —— 试验性安装永不误伤正式实例。

### 3.6 关键算法亮点

1. **对话框指纹**（`menu.mjs keyOf`）：从顶边框到最后选项做哈希，剔除光标符、勾选标记、预览框、空白差异（空格拼接而非换行，抗 terminal resize 重排）—— 保证"旧卡片永远无法回答长得像的新对话框"；
2. **游标 lag 窗口**：`at` 恒落后 10 分钟 + `seen` 列表兜底，解决日志条目"时间戳相同/晚写"的漏读问题；tail 读 1500 行不够（`complete=false`）自动整文件重读；
3. **多视图终端归一**（`views.mjs`）：终端身份 = `core id|tN`（跨 session、跨机器别名均归一），由 `owns()` 决定唯一跟随者，`identity()` 用于存活判断 —— 避免 herdr-app 多视角导致的重复房间；
4. **meter 缓存**：`lastKnown`（过期后台刷）+ `knownSoon`（首次限时等待）—— 页脚永不因探测而卡住发帖，也几乎不重复执行 `claude -p` 类命令；
5. **graceful degradation 无处不在**：飞书卡片被拒→纯文本；折叠卡被拒→普通帖；HTML 被 Telegram 拒→纯文本；`sips` 不在→默认头像；hook 不在→读屏；reader 不可读→发屏幕；图片上传 3 败→文字说明继续。

### 3.7 安全模型

- **owner-only**：飞书 open_id / Telegram user id 白名单，非 owner 消息忽略并记日志；按钮同样校验操作者；
- **配对**：6 位码 15 分钟 5 次限试，成功后写 config.env 并重启为 owner 身份；
- **秘密管理**：config.env / user-token.json / hook 均 0600 权限；日志显式不落 token；`askSecret` 无回显输入；服务环境变量白名单；
- **hook 注入的边界**：只增删带 `herdrchat/hooks/ask.sh` 标记的条目，首次改动自动备份原文件，读取失败的文件绝不动；
- README 明示信任边界：终端内容经过飞书/Telegram 服务器；Telegram bot 聊天非端到端加密；聊天里的按钮等价于终端按键授权。

---

## 4. 测试与工程实践

- **测试**：`node --test`，22 个文件约 2,500 行。`test/fakes.mjs` 提供假 chat（记录全部调用）、假 hc、假 reader、内存 store —— `engine.test.mjs` 在纯假环境验证内核行为与两份契约的 `checkChat/checkReader`；其余按模块单测（菜单解析、游标、格式转换、服务文件生成、hook 安装、文档一致性 `docs.test.mjs`、skill 一致性 `skill.test.mjs`）。`quick` 参数把所有等待压缩到 5ms，测试飞快；
- **CI**（`.github/workflows/ci.yml`）：Node 20/22/24 矩阵跑测试 + `smoke.sh` 真实安装冒烟（打包→全局安装→skill 安装→跑首步命令）；
- **发布**：semantic-release 自动版本 + CHANGELOG + conventional commits（`fix:` / `feat:` / `chore(release)`）；
- **文档**：README 双语 + `docs/guide.md`（419 行，全部设置与原理）+ `docs/architecture.md`（扩展指南）；代码内注释密度极高、几乎每个模块头部是一篇设计说明 —— 可读性属上游水准。

---

## 5. 设计亮点总结

1. **契约驱动的双插件架构**，启动即校验，扩展路径清晰（新增 App/agent 均有 checklist）；
2. **四源数据融合**（hc 事件 + agent 日志 + hook/API + 屏幕）按可用性逐级降级，互为校验；
3. **"宁可重复、不可丢失"** 的投递语义贯穿始终（游标滞留、卡片重发、暂存消息、离线补收）；
4. **手机优先的通知治理**（活卡片编辑避免响铃、mute/silent 分级、置顶 + @、回执表情）—— 把 ChatOps 的噪音问题当作一等公民设计；
5. **防陈旧交互** 的对话框状态机（哈希身份 + 选项文本复核 + 点击前重读屏幕）；
6. **依赖极简**（1 个运行时依赖）+ 原生 fetch + 平台限速自律；
7. 运维完整：交互式 setup、配对码、系统服务、崩溃通知、日志体系、烟测。

---

## 6. 局限与可改进点

| 项 | 说明 |
|---|---|
| 平台覆盖 | 仅飞书（不含国际版 Lark）；Slack/Discord 未实现（架构已预留） |
| engine.mjs 体量 | 1,479 行单文件承载房间/发帖/对话框/总览/入口五大职责，模块内聚但可再拆（如 dialogs、posting 各自成模块） |
| Codex 日志定位启发式 | 同一 cwd 同一时刻启动两个 Codex 会话可能错配（文档已自认）；依赖进程存活时间推断 |
| Telegram 限速 | ~1 msg/s 全局队列，几十个终端时话题创建与新消息明显排队 |
| 轮询密集度 | LIVE 5s / ASKS 2s / SWEEP 60s 固定间隔，终端数量大时 hc 子进程调用量可观（每次 `hc read`/`get` 都是 execFile） |
| state.json 全量读写 | 每次 update 都是整文件读-改-写 + JSON 序列化，房间很多时有放大开销（当前规模足够） |
| 飞书头像 | 依赖 macOS `sips`，Linux 无头像定制 |
| hook 作用域 | hook 仅装在本机；远端机器需另行安装 herdrchat（文档已说明） |

---

## 7. 结语

herdrchat 是一个**定位精准、工程素质出色**的 ChatOps 工具：它不重新发明终端管理（全交 hc），只解决"离桌看管"这一个问题，并把它解决到手机通知铃声级别的细节。其双插件契约架构、"四源降级"数据策略和"宁重不丢"的投递语义，使它在 agent 生态快速变动（Claude Code/Codex/OpenCode 日志格式频繁演化）的环境下具备良好的适应性与可测试性；文档、测试、发布流水线的完备程度也显著高于同规模开源项目。

> 本报告基于对仓库全部源码（`bin/`、`lib/` 33 个文件）、文档（README 双语、guide、architecture）、测试与 CI 配置的逐文件阅读撰写。

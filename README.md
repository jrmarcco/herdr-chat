<div align="center">

# herdrchat

hc（[herdr](https://herdr.dev)）的聊天客户端：在飞书里看到并回答你的每一个 hc 终端。

[![CI](https://github.com/jrmarcco/herdr-chat/actions/workflows/ci.yml/badge.svg)](https://github.com/jrmarcco/herdr-chat/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/tag/jrmarcco/herdr-chat)](https://github.com/jrmarcco/herdr-chat/releases)

[简体中文](README.md) · [English](docs/README.md)

</div>

## 简介

**herdr-app 是 hc 的桌面与手机客户端；herdrchat 是 hc 的聊天客户端。**

[herdr](https://herdr.dev) 把你的编码 agent（Claude Code、codex、pi…）跑在长活的终端里，
herdr-app 把它们按 space/tab 组织。但要在外面用 herdr-app，手机得先够得着跑 hc 的机器
（VPN、内网穿透），在小屏幕上操作 agent 也不顺手。

herdrchat 把同样的终端搬进你手机上已有的聊天软件：它**主动向外出站长连接**到飞书，
所以网络侧零配置——不需要公网 IP、不需要开端口。每个 herdr-app 的 tab 对应一个飞书群；
agent 工作时你实时读到它说的每句话，它提问时你点按钮回答，你打字/发图就注入终端；
直聊里一张总览卡片看全部终端。

核心能力：

- **活卡片镜像**：每轮一张 `⌛️ Working…` 卡片，每 5 秒原地编辑增量（工具调用、你的话、
  图片），不刷消息不响铃；轮次结束终答作为新消息到达；
- **对话框三通路**：agent 自报（hook / 自身 API）优先，按原话和原选项生成可点按钮；
  其次读屏幕的编号菜单（无编号、但屏幕自己声明「Enter 前进 · Esc 取消」的确认框也合成按钮，忠实转述）；再不行发整屏 + `/answer`、`/enter`、`/esc`。点击前复核屏幕，旧卡绝不误按；
- **输入双向**：群里打字即注入终端；图片经 `hc put` 放到终端所在机器，路径随文字一起进终端；
- **总览与命令**：直聊里 `/ls` 总览、`/new` 开终端、`/kill` 结束、`/sessions` 调视野、`/clean` 清理；
- **每帖页脚**：`claude · claude-opus-5-5 · main · context 23% (234k) · 5-hour 2% · 7-day 57%`
  ——harness、模型、分支、上下文水位、账号限额，由 herdrchat 自行采集，不花模型调用；
- **绝不丢内容**：发过的按条目 id 记住（`state.json`），重启续跑、活卡片续编；新群自动回填
  历史会话（文件 + 折叠卡）；
- **服务化**：`herdrchat install` 装成 launchd / systemd 用户服务，开机自起、崩溃自拉；
  `herdrchat update` 自更新并热重启。

agent 支持：**Claude Code、Codex、OpenCode** 全量镜像（对话、对话框、页脚数据）；
其他 agent（pi、shell 等）以屏幕镜像应答你的输入，直到它有了自己的 reader。

## 快速开始

### 需要什么

- **herdr-core**：herdrchat 建在它上面，没有它什么都做不了。先从 [herdr.dev](https://herdr.dev)
  装好（也可以让编程 agent 带你装：`/herdrchat setup` 会处理），hc 要在本机跑着至少一个
  session（`ls ~/.local/state/herdr-core`）。
- **macOS 或 Linux**：herdrchat 是单二进制，无其他依赖。
- **飞书团队**（非 Lark 国际版）：可以自建应用并发布的团队。

### 安装

```bash
# release tarball（macOS/Linux × x86_64/aarch64）：
#   https://github.com/jrmarcco/herdr-chat/releases
# ……或装了 Go 的话：
go install github.com/jrmarcco/herdr-chat/cmd/herdrchat@latest
```

### 配置与启动

最省事的路径（agent 还能替你点飞书后台）：

```bash
herdrchat setup     # 交互式：校验密钥 → 选 session → 启动 → 6 位配对码 → 问装不装服务
```

或者让 agent 做：对它说 `/herdrchat setup`（技能包见 [skills/herdrchat/](skills/herdrchat/)，
会用 Ego Lite 浏览器替你点飞书控制台）。手工步骤（建应用、填密钥、控制台开长连接事件与
回调、发布、配对）逐步说明在 [docs/guide.md](docs/guide.md#set-up-feishu)。

配对：`herdrchat pair` 给一个 6 位码，在飞书里发给 bot，bot 认你作 owner 并自动重启。

用 Claude Code 或 Codex 的话，推荐顺手 `herdrchat hooks install`：它们的提问/授权会用
**它们自己的原话和选项**进聊天，点按钮直接回写，全程不读屏（细节见
[docs/guide.md](docs/guide.md#answering-a-dialog-from-the-chat)）。

从 npm（Node）版换过来？停旧的、放新二进制、`herdrchat start`——配置与状态原样接管：
[迁移说明](docs/guide.md#migrating-from-the-node-version)。

## 使用

### 在聊天里

| 直聊（与 bot 私聊） | 作用 |
|---|---|
| `/ls` | 刷新总览（每个终端的状态、谁在等你、群链接） |
| `/new`、`/new <目录> [<session>] [<space>/<tab>] [命令]` | 开一个终端：裸 `/new` 出卡片（目录 + 可选去处 + agent/session 下拉）；给目录则直达（默认 `claude`，去处默认取最近 space 与目录名）；点名去处用 `<space>/<tab>`（两侧空格随意）；旧语法 `/new <session> <space>/<tab> [命令]` 仍可用 |
| `/kill`、`/kill <session> <地址>` | 结束一个终端：裸 `/kill` 出卡选（显示房间名）；点名直达 |
| `/clean` | 清理已结束终端的群与陈旧文件（先出预览卡，确认才删） |
| `/sessions`、`/sessions add/drop <name>` | 本机 hc session 一览 / 调整 bot 的跟随范围（写入设置并重启生效；不从聊天里造 core） |
| `/auth` | OAuth 设备流授权 bot「以你行事」：替你静音群、打标签、置顶 |

| 房间（某终端的群） | 作用 |
|---|---|
| 直接打字 | 注入该终端 |
| 发图片 | 落到终端所在机器，路径随文字进终端 |
| `/commands` | 该 agent 在这台机器上真实可用的斜杠命令目录（可点发送） |
| `/screen` `/answer <text>` `/enter` `/esc` `/trust` | 看屏幕 / 强制输入 / 按回车 / 按退出 / 代答 claude 的目录信任确认 |
| `/new` 的自动信任 | 新目录首句不再被信任确认拦下：`/new` 点名目录即视为信任，bot 起跑后代答 |
| `/history` | 该会话到目前为止的完整记录（文件 + 折叠卡） |
| 卡片按钮 | 回答 agent 的提问（多选、自由输入、Esc 都有） |

### 在命令行

```
setup      交互式配置一个 profile（聊天平台、密钥、session、首次启动）
pair       6 位码把 bot 交给 owner
init       生成初始 config.env
start      启动 bot（有服务走服务；否则在 hc 终端里跑保活循环）
stop       停止（所有形态）
status     运行形态、各 session core 状态、必填键、/auth、当日错误计数
logs       看日志（--errors 只看错误、--day 选日期、-n 取尾）
install    装成系统服务（launchd / systemd 用户单元）
uninstall  摘掉服务
hooks      查看/安装/卸载 PermissionRequest hook
update     拉取最新 release 原子替换二进制，并把 bot 热重启到新版
run        前台直接跑（服务与终端循环 exec 的就是它）
version    打印版本
```

全局 `--profile <name>` 开第二套实例（一 profile 一聊天软件账号）。

### 更新

```bash
herdrchat update          # 查 latest → 下载 tar.gz + checksums → sha256 校验 → 原子替换 → 热重启
```

`go install` 装的用 `go install …@latest` 后各 profile `herdrchat start` 一次。有新
release 时 bot 自己会提示 owner（只提示，不自动升级）。

## 架构

### 数据流

```
                         飞书（larkws 出站长连接：事件、卡片回调；无公网暴露）
                           ⇅ 消息 · 卡片 · 按钮 · 回执 · 补收

        ┌──────────────────────────────────────────────────────────────────
        │ internal/chats/feishu
        │ Chat 契约实现：卡片 schema 2.0、群/标签、
        │ /auth 设备流、离线补收、限速
        └──────────────────────────────┬───────────────────────────────────
                                       │ chats.Chat 契约
                                       │ internal/chats/chat.go
                                       ⇅

        ┌──────────────────────────────────────────────────────────────────
        │ internal/engine（编排核心）
        │  rooms      每 tab 一房间的生命周期
        │  posting    发帖管线 / 活卡片 / 图片预上传
        │  dialogs    对话框状态机（三通路 + 防陈旧）
        │  intake     聊天侧输入与直聊命令（注入 / /new /kill 卡 / hook 回写）
        │  overview   总览（去抖原地编辑）
        │  - 每终端一条串行车道（lane）保序
        └──────────────────┬───────────────────────────────┬───────────────
                           │                               │
                 readers.Reader 契约                HC 契约（core.HC）
                           │                               │
          ┌────────────────┼────────────────┐              │
          ⇅                ⇅               ⇅              ⇅
   readers/claude    readers/codex    readers/opencode   internal/hccli
   （transcript+      （rollout          （opencode api：   （hc CLI 封装：
    /context /usage）   日志启发式）       会话/表单）         事件流/输入/屏幕/
                                                             远端 exec --on）

          ⇐──────────── agent 自己的记录（各读各的，不装任何东西进 agent）────────────⇐
                                                              ⇅
                                                    hc (herdr-core) 终端
                                                     本机 + 连接的机器
```

另一条旁路：`herdrchat hooks install` 把内嵌的 `ask.sh`（逐字节复刻上游）装进 Claude
Code / Codex 的设置 → agent 提问时把请求落进 `~/.local/state/herdrchat/asks/` 并给终端打
`herdrchat.ask` 标签 → engine 凭标签发卡、你的点击写回 decision 文件。协议与 Node 版完全
兼容，两个版本可共存交接。

### 两条插件缝

- **聊天平台缝**（`internal/chats/chat.go` 的 `Chat` 接口，34 个成员）：engine 对每个平台
  一视同仁；平台一切私有能力（飞书的租户标签、头像、以你行事）都留在自己的包里。
  core/engine 永不 import 平台，平台也永不 import engine。
- **agent 缝**（`internal/readers/reader.go` 的 `Reader` 接口 + 可选能力小接口：对话框/
  回答/meter/标题）：hc 报的 `status.app` 挑选 reader；前台进程校验防状态残留误判。

engine 的 hc、readers、store、chat 全部注入，测试换成 `internal/fake` 三件套即可跑全部
行为用例，不碰 socket。

### 并发与可靠性

- **车道**：同终端的副作用全序串行，跨终端并行互不阻塞；
- **游标**：按条目 id + 时间戳双去重，LAG 10 分钟防晚写漏读；发帖失败游标不推进——
  **宁可重复，不可丢失**；
- **状态**：`state.json` 读-合-写全量 + 临时文件 rename 原子替换；损坏文件备份为
  `corrupt-<ts>` 并有声报错，绝不静默丢弃；
- **崩溃恢复**：panic 带栈落日志 → owner 收 🔴 卡 → 服务/终端循环拉起 → 起来后 ✅ 回归卡；
- **缓存全有界**：上传图 500 / 对话框卡 200 / 按钮 300 / seen 2000。

### 代码索引

一个 Go module，约 2.5 万行；运行时唯一外部依赖是飞书官方 SDK
（`github.com/larksuite/oapi-sdk-go/v3`）。

| 路径 | 职责 | 关键文件 |
|---|---|---|
| `cmd/herdrchat/` | 进程级入口：装配 CLI、忽略 SIGPIPE、panic 记日志退出 1 | `main.go` |
| `internal/cli/` | 全部子命令；依赖全注入（env/home/TTY/时钟/HC/服务），测试不碰真实环境 | `cli.go`（分发+usage）、`run.go`（bot 接线）、`setup.go`、`update.go`、`start.go`、`install.go`、`status.go`、`logs.go`、`pair.go`、`initcmd.go`、`hookscmd.go` |
| `internal/engine/` | 引擎：对每个聊天平台、每个 agent 都一样的编排 | `engine.go`（起停/事件接线/定时器）、`rooms.go`、`posting.go`、`dialogs.go`、`intake.go`（输入注入与直聊命令 /new、/kill、/sessions）、`overview.go`、`pairing.go`、`commands.go`（/commands 目录）、`clean.go`、`update.go`（新版提示） |
| `internal/core/` | 纯类型与算法（无副作用） | `types.go`、`errors.go`（错误分类学）、`lanes.go`、`cursor.go`、`menu.go`（屏幕菜单解析与 Enter/Esc 确认合成）、`views.go`（多视图拓扑）、`meter.go`、`hc.go`（HC 契约） |
| `internal/chats/` | 聊天平台插件缝 | `chat.go` |
| `internal/chats/feishu/` | 飞书实现：larkws 长连接、卡片、API、/auth | `chat.go`、`cards.go`、`sdk.go`、`userauth.go` |
| `internal/readers/` | agent 插件缝 + 注册表 | `reader.go` |
| `internal/readers/claude/` | 读 `~/.claude` transcript；`/context`、`/usage` 出页脚 | `claude.go`、`ask.go` |
| `internal/readers/codex/` | 读 `~/.codex` rollout 日志（启发式定位）；Plan/异步提问状态机 | `codex.go`、`questions.go` |
| `internal/readers/opencode/` | 走 `opencode api`（会话/消息/表单/权限） | `opencode.go`、`dialog.go` |
| `internal/hccli/` | hc CLI 封装：每次调用带超时、剥 `HC_TERMINAL_*`、16MB 上限；事件流跟随（10MB 缓冲）；能力探测 | `hc.go`、`events.go`、`home.go`、`capability.go` |
| `internal/asks/` | hook 的 asks 协议（req/ans 文件、标签、stray 认领） | — |
| `internal/hooks/` | `herdrchat hooks`：ask.sh 内嵌逐字节；只动带标记的条目；备份/原子写 | — |
| `internal/service/` | launchd/systemd 用户服务生成与控制；env 白名单 | `service.go`、`templates.go`（嵌入 `templates/` 下的 plist/unit 模板） |
| `internal/config/` | profile 布局（XDG/HERDRCHAT_HOME）、config.env 解析与逐行改写 | — |
| `internal/state/` | state.json schema 兼容、原子写、损坏备份 | — |
| `internal/cliversion/` | goreleaser `-X` 注入的版本变量 + 点分比较（预发布/伪版本算旧） | — |
| `internal/xlog/` | 单行紧凑日志、按天文件、14 天保留、TTY 着色 | `handler.go`、`daily.go` |
| `internal/fake/` | 测试三件套：FakeChat / FakeHC / FakeReader | — |
| `testdata/screens/` | 28 份真实抓屏，锁死 `menu.go` 解析行为（新增语料自动进 golden） | — |
| `scripts/` | lint 两档配置（CI 阻断/报告）、git hooks | `lint/.golangci.yaml` |
| `skills/herdrchat/` | agent 技能包：`/herdrchat setup/status/fix/…` | `SKILL.md`、`references/` |
| `.goreleaser.yaml` + `.github/workflows/` | 发布链路：tag `v*` → CI（vet+race）→ goreleaser 四平台产物（`herdrchat update` 的下载源） | — |

常用命令：`make lint`（基线档，CI 阻断）、`make lint-strict`（报告档）、
`go test -race ./...`（17 包全绿是合并前提）；发版：`make tag-patch|tag-minor|tag-major`
（从最新 tag 推算并推送，`make tags`/`make untag` 查看/灭火）。

### 扩展

**添加一个聊天平台**：

1. 先读该平台官方文档，逐行对照 `Chat` 契约：房间怎么建、消息怎么编辑、按钮回调、
   回执、图片双向、怎么保持安静、限速多少；写清它做不到什么、用什么顶替；
2. 建 `internal/chats/<app>/`，实现 `chats.Chat`（编译期检查
   `var _ chats.Chat = (*Chat)(nil)`），API 调用集中在旁边的 `sdk.go`，启动时过
   `CheckChat`；
3. 给出 `Requires`（config.env 必填键），接进 `internal/cli` 的 `ready()`；
4. 平台的键补进根目录的 `config.env.example`；
5. 测试：`CheckChat` + 自有消息格式转换；
6. 加 `skills/herdrchat/references/<app>.md`（后台配置步骤）；
7. 用独立 profile 跑（`--profile <app>` + `HERDRCHAT_CHAT=<app>`），不碰在跑的 bot。

**添加一个 agent**：

1. 摸清它怎么把会话落盘：日志在哪、怎么定位运行中终端的那份、一轮的最后一条怎么标；
2. 建 `internal/readers/<agent>/`，构造器接好共享件（文件、shell、asks、meter 缓存），
   实现 `Reader` 契约（`App/Processes/After/Rest`）；对话框、回答、meter、标题是可选
   小接口，有则实现；
3. 在 `internal/cli/run.go` 的装配处注册（`CheckReader` + `Registry.Register` 会校验）；
4. 带样本日志写测试；
5. `docs/guide.md` 的 readers 表加一行。

## 文档

| 文档 | 内容 |
|---|---|
| [docs/guide.md](docs/guide.md) | **用户手册**：每个设置项、房间细节、对话框三通路全解、飞书后台手工配置、迁移、/auth、limits |
| [docs/README.md](docs/README.md) | 本 README 的英文版 |
| [docs/history.md](docs/history.md) | **重写工程总结**：从 Node 版重写为 Go 的目标、决策、里程碑结果（原六份规划文档的精炼，全文在 git 历史） |
| [skills/herdrchat/](skills/herdrchat/) | agent 技能包：装好后说 `/herdrchat` 即可用 |
| [THIRD-PARTY-NOTICES](THIRD-PARTY-NOTICES) | 上游归属：zhangxiangliang/herdrchat（MIT）、cobanov/herdrchat（Apache-2.0）、飞书 SDK |

## 隐私与安全

- 终端里显示的内容会发到飞书的服务器；
- 只有 `config.env` 里设置的 owner 能向终端输入，其他人一概忽略；
- App Secret 像密码保管：谁拿到它谁就能操作你的终端；
- bot 打的租户标签全团队可见（`HERDR_BOT_TAGS=off` 关闭）；
- 装了 hooks 后，聊天里点一下即可批准 Claude Code / Codex 的请求，与在终端按键等效。

## 卸载

每个 profile `herdrchat uninstall` → `herdrchat hooks uninstall` → 删二进制
（`command -v herdrchat`）→ 删 `~/.config/herdrchat` 与 `~/.local/state/herdrchat` →
删飞书应用。

## 许可证

MIT © [zhangxiangliang](https://github.com/zhangxiangliang)（上游原版作者）。本项目是其
Go 重写，借鉴与复用的归属见 [THIRD-PARTY-NOTICES](THIRD-PARTY-NOTICES)。

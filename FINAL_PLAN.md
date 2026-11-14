# herdrchat-Go 最终实现方案（FINAL_PLAN.md)

> 本文档是执行的**总纲**：确认基线决策、把 cobanov 增强项并入 Go 结构、处理上游版本漂移、给出修订后的里程碑与启动顺序。
> 与既有三份文档的关系：[spec.md](spec.md) 定"重写成什么样"（**执行主线，不变**）；[optimization.md](optimization.md) 定"哪里比原版更好"（全部采纳）；[reuse.md](reuse.md) 定"哪些直接拿来用"（落地顺序不变）；本文定"**增强项怎么并入、按什么顺序、验收到什么程度**"。

---

## 0. 基线决策（已定，不再讨论）

| 决策点 | 结论 |
|---|---|
| 网络模型 | zhangxiangliang 方案：主机常驻进程 + **出站**长连（飞书 larkws）/长轮询（Telegram），零入站暴露。已固化于 spec §1/§9 |
| 语言与形态 | Go 单二进制（`github.com/jrmarcco/herdr-chat`，二进制名 `herdrchat`，Go 1.27，macOS+Linux） |
| 兼容承诺 | spec §1 硬性要求全部保留：config.env / state.json / asks 协议 / ask.sh 逐字节 / CLI 语义。Node 版用户原地换二进制无损迁移 |
| 重写增强 | optimization.md A–E **全部采纳**（不变量 A 是红线；B1 拆巨石、B3 单二进制、C1 分 chat 队列、D4 有声恢复、D6 假 hc 对拍为重点） |
| cobanov 增强 | 仅采纳知识级移植（本文 §2 的 A–E 项 + §2.6 backlog），不引入其 SSH 直连/PWA/推送中继——与出站架构无关或冲突 |
| 上游基线 | 功能冻结在 **v1.10.2**（spec/黄金测试的锚点）；1.11–1.12 的新功能按 §3 漂移策略选择性吸收 |

## 1. 与前序 cobanov 优化方案的映射

前期方案（P1/P2/P3）与本资料包里程碑的对应关系：

| 前期方案 | 内容 | 最终落位 |
|---|---|---|
| P1-① 斜杠命令目录 | cobanov `src/lib/slashCommands/` | §2.A → M4 |
| P1-③ 屏幕语料库 | cobanov fixtures 思路 | §2.C → M3 出口标准 |
| P1-④ 能力门控 | cobanov `version.ts` 模式 | §2.D → M1（地基） |
| P2-② 结构化 items/子代理卡 | cobanov `threadItems` + `subagents/` | §2.B → M4 |
| P3-⑤ 错误分类学 | cobanov `AppErrorCode` + `timeouts.ts` 纪律 | §2.E → M1（地基） |
| P3-⑥⑦ 可选件 | blockedPrompt / attach 流式 | §2.6 backlog → M8 后 |

明确不移植（维持前期结论）：原生 SSH/SwiftTerm 模块、PWA 与主题、APNs 推送中继、多主机 UI（IM 总览已覆盖）、usage/meter 双份实现、事件流（spec §5.2 已等价）。

---

## 2. cobanov 增强项并入 Go 结构（spec §2 的 delta）

```
internal/
├── hccli/
│   └── capability.go        [新·D] hc 版本/能力探测与门控
├── core/
│   ├── errors.go            [新·E] 错误分类学（哨兵错误 + 用户话术）
│   ├── commands.go          [新·A] 斜杠命令目录
│   └── (engine 拆分按 optimization B1：rooms/posting/dialogs/intake/overview)
└── readers/
    └── claude/
        └── items.go         [改·B] 结构化条目：工具压缩行、子代理/工作流卡
```

### 2.A 斜杠命令目录（来源：cobanov `src/lib/slashCommands/{builtins,catalogue,discover,parse}.ts`）

**做什么**：枚举这台机器上该 agent **真实存在**的斜杠命令，IM 里可发现、零参数命令可一键发送。

```go
// internal/core/commands.go
type Command struct {
    Name, Desc, ArgsHint string
    Source string // builtin | user | project | plugin | skill
    NoArgs bool    // true → 卡片按钮直接 hc send
}
func Catalog(agent, cwd, machine string) ([]Command, error) // 缓存 10min，机器维度
```

- **数据源**（本机直读或 `hc exec --on`，cobanov 走 SSH 都能做，本地更简单）：
  - claude：`claude --version` 门控内置命令表（版本对应）；`~/.claude/commands/*.md`、`<cwd>/.claude/commands/*.md`、plugins、skills（front matter 的 name/description/argument-hint，**手写极简解析，不引 YAML 库**）；
  - codex：`~/.codex/prompts/*.md` + 内置表；
  - opencode：探测 `opencode api command.list`，可用则用。
- **IM 入口**（intake.go 新增，不破坏既有命令）：房间内 `/commands` → 卡片（命令 + 说明 + 参数提示，`NoArgs` 命令为按钮）；裸 `/` 提示引导。直聊 `/commands <space>/<tab>` 同理。
- **契约影响**：零——复用 `PostCard`/按钮回调；回调 value 编码 `cmd:<name>`，点击 → 校验仍在目录中 → `hc send "/<name>"`。
- **测试**：tmpdir 摆放 commands/skills 文件的 golden 表驱动。
- **里程碑**：M4 后半（readers 就位后 per-agent 才有意义；claude 先行）。

### 2.B 结构化条目与子代理/工作流卡（来源：cobanov `threadItems`、`subagents/{taskNotice,delegationEnd,workflowRun}.ts`、compact tool activity）

**做什么**：reader 产出从纯文本升级为结构化条目，IM 里呈现"子代理开始/结束"卡片与更干净的工具活动行。

- `Item` 扩展（spec §3.2）：增加 `Card *Card` 字段，`Card = {Kind: subagent|workflow|phase, Title, State: started|done|failed, Summary string}`。**不入 state.json**（仅发帖管线内存在），零兼容风险。
- claude reader（items.go）：`Task` 的 `tool_use` → Card(started, 摘要=prompt 截断)；对应 `tool_result` → Card(done|failed, 摘要=结果截断)；连续工具行合并升级为"紧凑工具活动"（cobanov 的 compact 规则：同类相邻折叠，计数显示）。
- 渲染：`item.Card != nil` → `PostCard`（tone 映射：started=working、done=done、failed=bad）；飞书/TG 两端复用现有卡片模板。
- codex：`item_completed` 的子代理类事件同规则（有则做，无则不做——以 golden 样本为准）。
- **测试**：readers golden 扩展（transcript 样本含 Task 调用 → 断言 Card 序列）。
- **里程碑**：M4（与 readers 同期，同批 golden）。

### 2.C 屏幕语料库（来源：cobanov `src/lib/__tests__/fixtures/screens/` 模式）

**做什么**：menu.mjs 是全项目最脆一环（TUI 逆向正则），用真实屏幕样本锁死行为。

- `testdata/screens/*.txt`：claude（权限/多选/预览框 beside/framed 与 unframed）、codex（`›` 光标风格/`• Running …` 下对话框）各版本真实抓屏。
- `menu_test.go` 表驱动：逐文件断言 `menuOf` 输出（key/options/multi/free/beside）；`screen_test.go` 同法（tidyScreen）。
- cobanov 仓库的 fixtures 可直接借用（见 §5 许可证）；skill 的 references 增加一节"如何抓样本"（`hc read <addr> --lines 40` 落盘）。
- **里程碑**：M3 出口标准追加"≥20 个真实样本，menu 解析全绿"。

### 2.D hc 能力门控（来源：cobanov `src/lib/herdr/version.ts` 模式）

```go
// internal/hccli/capability.go
type Capabilities struct {
    HCVersion    string
    HasTerminalAttach bool  // hc terminal attach（0.9+）
    EventsShape  int    // 事件流 schema 特征（防御性）
}
func Probe(session string) Capabilities // 启动时 + 每 60s cores 刷新时
```

- 探测失败 → 全部按"最低能力"降级运行，`status` 显示；**绝不因探测失败拒绝启动**。
- 首个消费者是 §2.6-G（attach 流式屏幕）；当前主要价值是 `status` 自诊与未来特性的安全开关。
- **里程碑**：M1（地基件，一次写对不再动）。

### 2.E 错误分类学（来源：cobanov `protocol.ts` AppErrorCode + `timeouts.ts` 纪律）

- `internal/core/errors.go`：哨兵错误集（对照 cobanov 清单裁剪适用项）：`ErrTranscriptChanged / ErrTranscriptUnreadable / ErrSessionAmbiguous / ErrLogMatchAmbiguous / ErrMachineDown / ErrTerminalFinished / ErrHookAbsent / ErrStateCorrupt …` + `UserWords(err) string`（IM 里一句话人话）。
- 与 optimization D4 联动：state 损坏 → `ErrStateCorrupt` → 备份 + 有声恢复。
- 超时纪律（timeouts.ts 的教训）：hccli 每个调用**必须**带 context 超时（spec §15 已列数值表，执行为代码评审红线：无 deadline 的调用不予合并）。
- `/herdrchat fix`（skill）按错误码分诊的话术留待 M7 顺带更新。
- **里程碑**：M1（地基件）。

### 2.6 backlog（M8 后，不进主线）

- **F. blockedPrompt 从 transcript 读**：Claude reader 的 `Dialog()` 在 hook 缺席时从 transcript 解析 blocked 弹窗（cobanov `transcript/blockedPrompt.ts`），把"读屏"从第三优先级再往后挤。
- **G. `hc terminal attach` 流式屏幕**：依赖 2.D 门控；替代 `ScreenTail` 轮询，"给我看屏幕"与兜底发屏秒回。

---

## 3. 上游漂移策略（v1.10.2 → 1.12.x）

上游两天连发 12 版，**不追新、只吸收协议级变化**：

| 上游版本 | 变化 | 处理 |
|---|---|---|
| 1.11.0 | `herdrchat update`（npm 自更新）+ 更新通知 | Go 版重设计：goreleaser 发布 + `herdrchat update` 子命令（下载 release 二进制 → 原子替换 → 服务自动重启），作为 **M7.5**；bot 内"有新版"提示照抄语义 |
| 1.12.0 | footer 显示 harness/model；OpenCode 显示 GLM Coding Plan 限额；若干 footer 修复 | GLM 限额并入 **M4** 的 opencode meter（golden 样本从上游 test 提取）；footer 措辞以 1.12 为准 |
| 其他小修 | 文案/崩溃修复 | 每里程碑收尾 diff 上游 CHANGELOG，凡涉及**常量表（spec §15）或 state 字段**的改动记入本节"漂移日志"，其余忽略 |

漂移日志（执行时追加）：_（空）_

## 4. 修订后的里程碑与验收

spec §14 的 M1–M8 为主干，delta 如下（**加粗**为新增出口标准）：

| M | 内容 | 新增 delta |
|---|---|---|
| M1 | 骨架 + 契约 + Fake 基建 | **errors.go、capability.go 就位；hccli 无 deadline 调用红线生效** |
| M2 | 引擎（房间/发帖/游标/活卡片/总览/入口） | — |
| M3 | 对话框三通路 + hook + 菜单解析 | **§2.C：`testdata/screens/` ≥20 真实样本，menu/screen 表驱动全绿** |
| M4 | readers ×3 + meter/footer | **§2.B：Item.Card + claude 子代理/工作流卡 golden；compact 工具行；§2.A：claude/codex 命令目录 + `/commands` 卡片；GLM 限额（上游 1.12）** |
| M5 | Telegram | —（C1 分 chat 队列已计入） |
| M6 | 飞书 | — |
| M7 | CLI 全命令 + 服务 + hooks 安装器 | — |
| **M7.5** | **`herdrchat update`（goreleaser 链路）** | **bot 更新提示语义对齐上游 1.11** |
| M8 | 端到端验收 + 文档 + 迁移说明 | **THIRD-PARTY-NOTICES、skill 增补（抓样本/fix 分诊）**；之后进入 §2.6 backlog |

验收总口径不变（spec §13），补充两点：
1. 与 Node 版并跑对拍（D6 假 hc 剧本）时，**C1 分 chat 队列与 A/B 新特性造成的行为差异属预期差异**，对拍脚本需标注豁免清单；
2. 新增 golden：命令目录（fixture 目录扫描）、Card 序列（readers 样本）、屏幕语料（§2.C）。

## 5. 依赖与许可证

- **运行时依赖仍只有一个**：`github.com/larksuiteoapi-sdk-go/v3`。A 项的 front matter 解析手写（几十行），不为它引第二个依赖。
- `THIRD-PARTY-NOTICES`（M8 收尾，可提前建）：
  - zhangxiangliang/herdrchat —— MIT（spec 大量行为规格与全部 golden 测试的来源）；
  - cobanov/herdrchat —— Apache-2.0（§2.A/2.B/2.C 的算法与正则知识来源、屏幕样本借用；保留其 NOTICE 归属说明）。

## 6. 风险清单

| 风险 | 缓解 |
|---|---|
| 上游高速漂移破坏 golden 锚点 | 基线冻结 1.10.2 + §3 漂移日志；golden 从 testdata/ 固定版本提取，不跟上游 HEAD |
| 菜单语料不代表新版本 TUI | skill 内置"抓样本"指引；CI golden 失败即视为行为变化信号而非测试噪音 |
| Item/Card 扩展破坏 state 兼容 | Card 仅存在于发帖管线，不落 state（§2.B 已注明）；code review 检查项 |
| 双版本共存期互操作 | ask.sh 逐字节复用 + asks/state 路径不变（spec §1 兼容承诺），迁移说明 M8 交付 |
| 命令目录被 agent 版本变化破坏 | 内置表按 `--version` 门控（cobanov 同法）；解析失败 → 目录静默缺项，不影响主流程 |

## 7. 启动顺序（第一个两周，M1 → M2 中段）

1. **Day 1–2**：仓库脚手架按 spec §2 落位；`internal/config`（config.env 解析/setConfigValue）、`internal/state`（原子写 + schema）、`internal/logging`（slog + 按天文件）；`internal/core/errors.go`、`internal/hccli/capability.go` 一并就位；
2. **Day 3–4**：`internal/hccli`（exec 封装 + 事件跟随器，10MB Scanner）+ `internal/core/lanes.go`；对照 `testdata/node-tests/hc.test.mjs` 转录首批表驱动测试；
3. **Day 5**：Chat/Reader 契约接口 + checkChat/checkReader + Fake 三件套（对照 fakes.mjs）——M1 出口（契约测试绿）；
4. **Week 2**：engine 骨架（rooms/posting/cursor/活卡片，optimization B1 的拆分从一开始就按模块落文件），跑 `engine.test.mjs` 的对等用例——推进 M2。

> 一句话总结：**spec.md 怎么写就怎么实现；optimization.md 全部采纳；本文 §2 的 A–E 在指定里程碑并入；上游只跟协议不跟功能；一切等价性以 testdata/ 的 golden 对拍为准。**

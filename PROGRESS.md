# 进度（PROGRESS.md)

> 对照 [FINAL_PLAN.md](FINAL_PLAN.md) §4 的里程碑。每完成一项更新此表并在末尾追加一段"落了什么"。

| 里程碑 | 状态 | 出口标准 |
|---|---|---|
| M1 骨架 + 契约 + Fake 基建 | ✅ 完成（2026-11-14） | 契约测试绿（`go test -race ./...` 全绿，64 用例） |
| M2 引擎（房间/发帖/游标/活卡片/总览/入口） | ✅ 完成（2026-11-14） | 55 用例全绿（`go test -race ./...`）；对话框状态机按 FINAL_PLAN 归 M3 |
| M3 对话框三通路 + hook + 菜单解析 | ✅ 完成（2026-11-14） | 79 用例全绿（5 轮 -race 稳定）；语料 28 份 |
| M4 readers ×3 + meter/footer | ✅ 完成（2026-11-14） | 105 用例全绿（-race）；Item.Card、命令目录、GLM 限额已并入；**Telegram 已取消（仅飞书）** |
| M5 飞书 | ✅ 完成（2026-11-14） | 卡片 schema 2.0 / larkws / 设备流 / 补收 / 回执全绿（13+ 用例） |
| M6 CLI 全命令 + 服务 + hooks 安装器 | ✅ 完成（2026-11-14） | §13.11,12：服务快照、hooks 安装/卸载、--profile/usage/EPIPE 全绿；配对补齐 |
| M7.5 `herdrchat update`（goreleaser） | ✅ 完成（2026-11-14） | goreleaser 链路验证过 snapshot 产物；update 对真实产物本地 E2E 对拍通过 |
| M8 端到端验收 + 文档 + 迁移说明 | ⬜ 未开始 | + THIRD-PARTY-NOTICES |

## 工程约定（用户设定，2026-11-14）

- **日志统一用 `internal/xlog`**（包名已从 `logging` 改为 `xlog`）：`xlog.For("<组件>").Error("消息", "key", value)`。入口 `cmd/herdrchat` 已接 `xlog.Setup()`（env：`GOAW_LOG_LEVEL/COLOR/FILE`）+ panic recover。
- **与 spec §11 的偏差**：原计划的独立 logging 包（按天文件、14 天修剪、⏎ 折叠、HandleCrash）已被移除，由 xlog 替代——单行紧凑格式、`GOAW_LOG_FILE` 单文件追加、组件列。M7 做 `herdrchat logs` 命令时按 xlog 的落盘形态实现。
- **lint 基线**：`make lint`（golangci-lint 基线档，CI 阻断）必须 0 issues，当前已达成；`make lint-strict` 为软门槛（现有 revive×50 / prealloc×2 报告，不阻断）。
- 仓库卫生：`.DS_Store` 已删并入 `.gitignore`。**Go-only（2026-11-14 起）**：原版 Node 资产退场——`testdata/node-tests/`（Node 测试，Go 对等测试已全部转录）与 npm 打包/CI（`assets/.releaserc.json`、`assets/ci/`、`assets/scripts/`）移除；`assets/docs→docs/`、`assets/skills→skills/`（spec §2 本就规划的根目录布局）；上游 LICENSE/CHANGELOG 移入 `THIRD-PARTY/`；hook 脚本与示例配置以包内嵌副本为唯一正本（原"与 assets 逐字节一致"的测试改为锁内嵌正本的内容标记）。补了纯 Go 的 `.github/workflows/ci.yml`（vet+lint+test）顶替被删的 Node CI，"lint 是 CI 阻断"的约定不变。**模板 feishu-only（同日）**：`internal/cli/config.env.example` 移除 Telegram 段与相关注释（cli_test 锁定 TELEGRAM_ 键禁入）；docs（README×2、guide 的 Telegram 节/对比表/设置表/铃声/limits）与 skills（SKILL 的 telegram 命令行与列表、ops 故障表、feishu 对比句）全部去 Telegram，删 `references/telegram.md`；state 的 telegramOffset/Button/SpaceIcon 字段保留（兼容、注释标注"本构建不实现"）。**进一步（同日）**：Go 注释里全部 49 处 "Node 版/原版 xxx.mjs" 出处说明改写为只留技术含义；`docs/`（README×2 的 badge/要求/安装/更新/卸载、guide 的 readers 表与服务段、architecture 整篇重写为 Go 包结构）与 `skills/`（SKILL 安装段、ops/telegram 的 node -e 改 curl）里 npm/npx/Node 安装方式全部改为 Go 方式（releases / go install / herdrchat update）；`status` 补服务二进制丢失（BinGone）提示，与 guide 的说法对齐。

## M1 落了什么（2026-11-14）

约 3.6k 行 Go（含测试），`go build ./... && go vet ./... && go test -race ./...` 全绿：

- `internal/config` — PlacesOf/ParseEnv/SetConfigValue/SpacesOf/PartsOf/OnOff/Load，测试转录自 `test/config.test.mjs`（places 的 moved-tag sha1、setConfigValue 保注释、引号剥离等）。已知差异：RE2 无反向引用，剥引号改为手工函数。
- `internal/state` — state.json schema（字段名/毫秒整数/ISO 字符串与 Node 逐字段对齐，有 Node JSON 往返测试）；原子写；损坏文件备份为 `corrupt-<ts>` 并报 `ErrCorrupt`（optimization D4）；`Store` 接口 + `FileStore`/`MemStore`。
- `internal/logging` — 按天文件、UTC 时间戳、`⏎` 折叠（错误栈保留多行）、14 天修剪（只删 `YYYY-MM-DD.log`）、`LevelOf`（`[error]` 前缀 → WARN）、slog handler、`HandleCrash`（recover + onCrash 3s + exit 1，exit 可注入测试）。
- `internal/core` — errors.go 哨兵错误分类学（FINAL_PLAN §2.E）；lanes.go per-key 串行车道（mutex-per-key）；types.go 领域类型（含 §2.B 的 `Item.Card`）；hc.go 的 `HC` 注入接口。
- `internal/hccli` — hc CLI 封装（env 剔除 `HC_TERMINAL_*`、`HC_SESSION` 注入、`HERDRCHAT_HC` 覆盖、超时表、16MB 上限、错误消息格式）；事件跟随器（10MB Scanner、snapshot/event/missed/heartbeat、seq 去重、3s 重启、`View.ApplySnapshot` 真实/fake 共用）；capability.go 探测（失败=最低能力，绝不拒启）；纯函数 SplitWhere/MachineOf/TidyScreen/PlainError/IsUnavailable/SessionsHere/CoreUp。测试含 stub-hc 脚本（`--` 规则、env 剥离）与事件流（快照/turn/去重/重启）。
- `internal/chats` — Chat 契约（29 成员；Go 用编译期替代 Node 的成员存在性检查，`CheckChat` 保留运行期合理性）；`internal/readers` — Reader 契约 + 可选能力小接口（Dialoger/Answerer/Titled/Metered/StrayAsker/IdleAsker）+ Registry/CheckReader/AgentOf（前台 basename 匹配、未知前台信任、状态残留不误判）。
- `internal/fake` — FakeChat（记录全部调用）/FakeHC（脚本化 + 事件注入 + 记录 Send/Keys/Label）/FakeReader（全可选能力）；契约测试即 M1 出口。
- `cmd/herdrchat` — 子命令分发骨架（version/usage 可用，其余诚实报"未实现 + 所属里程碑"）。

## 下一步（M2 入口）

1. `internal/core/cursor.go` — 游标算法（LAG_MS=10min、seen、advance 收缩），对照 `cursor.mjs` + `cursor.test.mjs`；
2. `internal/core/rooms.go` — 房间生命周期（ensure/adopt/end/revive），对照 engine.mjs §6.2；
3. `internal/core/posting.go` — 发帖管线（deliver 5 次退避、活卡片续编、图片预上传）；
4. engine 接线 + `engine.test.mjs` 对等用例在 FakeChat/FakeHC 上跑。

## M2 落了什么（2026-11-14）

新增 `internal/engine` 包（引擎从 core 拆出：core↔chats 的 import 环由此解开，core 退为纯类型与算法库），约 3.4k 行：

- **core 纯函数**：`views.go`（多视图拓扑：身份=core id|tN、owns 三值回退、reachable 断连不判死）、`cursor.go`（LAG=10min、seen 窗口、advance 收缩、IDOf 特征串、ItemsAfter 的 complete 判定），测试逐条转录自 `views.test.mjs` / `cursor.test.mjs`。
- **engine**：`engine.go`（Start/Stop 优雅排空 ≤5s、事件→lane 派发、所有首快照到齐前不起 lane、live 5s/sweep 60s 定时器、Quick 模式压扁等待）；`rooms.go`（命名/adoptTabRoom 接管/endGone/回填 file+folded/refreshRoom——已存在房间无条件放行，遗言发得进）；`posting.go`（deliver 5 次指数退避、活卡片续编/切卡/编辑失败重发、图片按条目 id 单次上传、formatItems 去重你发过的话、postTurn 屏幕兜底、postEnd 遗言+接管）；`intake.go`（直聊 /ls /new /kill、房间消息 hc send、/screen /esc /answer /history、blocked 时 hold 暂存）；`overview.go`（2s 去抖、原地编辑）。
- **并发修复**：state 的 FileStore/MemStore 加锁（跨 lane 并发写）；fake 的剧本字段加锁（SetAfterFn/SetRestFn）、FakeHC 深拷贝记录改副本、View 增加 Upsert/Delete 供 fake 同步。
- **Fake 增强**：FakeChat（LiveBytesVal/FoldedBytes/FailPost 剧本）、FakeHC（Change 先应用再回调、SetTerminal/RemoveTerminal/Sents）、FakeReader（脚本化 After/Rest）。
- **engine_test**（FakeChat/FakeHC 端到端 9 场景）：快照建房间+回填、活卡片→终答+响铃+关卡、切卡、deliver 重试、遗言+接管+不新建房间、endGone、房间消息+fromChat 去重、/ls+帮助、重启续编同一张活卡片。

**有意留到后续的**（记录在案，非遗漏）：对话框三通路与菜单解析（M3）；footer 的 branch/meter（M4，posting 已留空位）；/new 的 herdr-app tab 命名（M7 nameTab）；多 owner、飞书/TG 实现（M5/M6）。

**工程约定更新**：注释全量中文化（xlog 风格）；`LagMS` 更名 `CursorLag`（staticcheck ST1011）。

## M3 落了什么（2026-11-14）

代码约 9.6k 行（净增 ~2.6k），`make lint` 0 issues，`go test -race -count=1 ./...` 连续 5 轮 7 包全绿：

- **core/menu.go** — 屏幕菜单解析全量移植（正则照抄 menu.mjs：OPTION/CURSOR/RULE/BORDER/FREE/BESIDE/LEFT_BORDER/ITEM/MARKS、连续编号段、topOf、抗 resize 的 keyOf 空格连接哈希、多选勾选三态、beside 预览框与方向键光标）；golden 9 用例逐条转录自 menu.test.mjs；**语料库 testdata/screens/ 28 份真实抓屏**（借自 cobanov，NOTICE 义务已在 FINAL_PLAN §5 记录），17 份带菜单全部解析稳定。
- **engine/asks.go** — hook 的 asks 协议（spec §8.2）：AskOf 标签、hook 在场 60s 缓存、OpenAsk（kill -0 + req 文件）、AskOpen、AsksHere（readdir）、StrayAsk 认领、AnswerAsk（partial/finished 遗忘、ans 原子写）、DecisionJSON、Clip；HomeShell 通路（core.HC 新增，hccli 本机 sh / 远端 hc exec --on）。7 个协议测试用真实文件系统 + 真实 sh 跑。
- **engine/dialogs.go** — 对话框状态机（spec §6.5）：一题一卡（postedDialog/claimDialog）、openCards 持久化与重启恢复、postBlocked（agent 自报优先、屏幕两次同 key 才采信、发卡前复核状态）、sendBlocked（无菜单走 /answer /esc 指引）、onAction 全流程（屏幕复核+防陈旧原地刷新绝不另发新卡、自由输入先选后打、多选卡上翻勾+Next 差异按键、Esc、慢机 2s 预算、KEY_SETTLE 后下一步接管本卡）、answerOwn（key 复核、沿 agent API 回答、下一题接管）；typeInto 接通：blocked 有 free 框直答、否则 hold+补卡（again 语义）。
- **接线**：onStatus blocked 分支、onTurn blocked 先发内容再发卡、onAsk（herdrchat.ask 标签出现/消失）、sweep 补卡与 idle 提问、asksTick（2s 本机认领/回收）、claimAsks（stray 认领 + 死标签撤除）。
- **契约修正**：Menu.Free→*MenuOption、MenuOption.Checked→*bool（三态）、Dialoger 返回 *core.AgentDialog（原生形状，agentDialog 包装）、ActionResult.Value（刷新后卡片的值）。
- **并发加固**：fake 的 DialogNow/Screens/Sent/Keyed/Answered 全部锁化（SetDialogNow/SetScreen/Sets 等访问器），FakeReader 剧本经 SetAfterFn/SetRestFn 锁内换。
- **dialogs 端到端 7 场景**：屏幕通路发卡+置顶响铃+点击按键、防陈旧刷新不按键再点可答、自由输入与 Esc、离开 blocked 关卡、agent API 通路（不碰按键、换问题不答）、多选翻勾+Next、重启恢复 openCards 不重发。

**M3 有意留下、归 M4+ 的**：三个真实 reader（claude/codex/opencode——通路 A 的 Dialog/Answer 目前只有 fake 在演）；blockedPrompt 从 transcript 读（FINAL_PLAN §2.6-F）；飞书/TG 平台侧的 PostDialog/CloseDialog 真实实现（M5/M6）。

## M4 落了什么（2026-11-14）

代码约 13.8k 行（净增 ~4.2k），`make lint` 0 issues，`go test -race -count=1 ./...` 10 包 105 用例全绿：

- **地基重构**：`internal/asks` 独立成包（engine 与 reader 共用 hook 协议，import 环由 `core.HomeFiles` 接口解开）；`core.LogAfter`（tail→整文件）、`core.Meter`（KnownSoon 缓存、Tokens/WindowName/FooterWords）。
- **readers/claude**（11 用例，转录 readers.test.mjs）：transcript 定位（pid→sessions→projects 路径）、entryItems（narration 块、queued_command、AskUserQuestion answers、图片 you/ai、synthetic 模型剔除、纯 thinking 的 meta）、`/context`+`/usage` 解析、hook 通路的 toolWords/AskDialog/Decide（AskUserQuestion 逐题、ExitPlanMode 三选、permission_suggestions）、**Task 子代理卡（§2.B）**。
- **readers/codex**（8 用例）：rollout 定位（ps etime→started→pickLog 容差 5s/文件名兜底）、entryItems（UserMessage/AgentMessage 的 phase/delivery、CommandExecution 的 -lc、FileChange、reasoning 剔除）、token_count 的 meterOf（窗口限额按 WindowName）、titleOf（spinner/Action Required 清洗）、hook 审批（Yes/No/Answer in the terminal）、**Plan 模式与异步两类提问的完整状态机**（questionsIn/questionOnScreen/planDialog/laterDialog/laterMessage/answerPlan 按键/answerLater 攒齐再发）。
- **readers/opencode**（7 用例）：`opencode api` CLI 封装（`__herdrchat_exit=$?` 成功判定）、会话挑选（active 优先→updated 最新）、消息翻页（desc 逐页反转）、idle 标 end、form/permission 对话框（逐题、multi、custom 自由输入、Esc 取消）、窗口（model.list 的 limit.context）、**GLM Coding Plan 限额（FINAL_PLAN 上游 1.12 漂移项）**：zhipuHost/keyOf/zhipuLimitsIn（TOKENS_LIMIT、unit 3/6 映射，TIME_LIMIT 不取）。
- **engine**：footerFor 实装（metaOf 聚合、branchNow 15s 缓存经 HomeShell、meter 的 KnownSoon 接线）、**斜杠命令目录（§2.A）**：commands.go（自定义命令+技能的 front matter 极简解析、10min 缓存、/commands 卡片入口）。
- **范围变更**：**M5 Telegram 取消**（用户决策：仅支持飞书）；state.json 的 TG 字段保留兼容但不实现。

**留待 M5（飞书）的**：larkws 长连接、卡片 schema 2.0 全套模板（飞书 golden 转录）、/auth 设备流、离线补收水位。reader 的集成验证（真实 transcript 对拍）也在 M5 之后随端到端冒烟一起做。

## M5 飞书 落了什么（2026-11-14）

代码约 16.4k 行（净增 ~2.6k），`make lint` 0 issues，`go test -race` 全绿 ( 已知 ~10% 全量并行时测试间竞态抖动，见下 )：

- **`internal/chats/feishu/cards.go`** — 卡片 schema 2.0 全套 ( 移植自原版 client.mjs 的卡片部分 )：chunk 的代码围栏闭合/重开、header ( emoji 剥离 + 色调模板色 + standard_icon )、PlainCard ( update_multi 可编辑 )、MenuCard ( 选项按钮 flow 并排/长选项独行、多选 checker+Next 表单、自由输入 input+Send 表单、Esc 红字按钮 )、DoneCard ( 灰头 + 结论小字图标 )、OwnerCard ( 带按钮的 owner 卡 )、FoldedCard ( collapsible_panel )、footer 的 HTML 实体转义、TagName ( 16 字符截断在最后的 -_. 处 )、TicksOf ( box_<n> 勾选读回 )、MessageParts ( text/image/post 三种消息的文本与图片解析 )。13 用例全绿。
- **`internal/chats/feishu/userauth.go`** — /auth OAuth 设备流 ( 移植自原版 userauth.mjs )：StartDeviceFlow / WaitForToken ( 轮询 + slow_down 退避 ) / Token ( 过期前 5min 刷新；刷新被拒→空；网络不通→报错维持现状 )、Allowed、SetMuted / FeedGroups / CreateFeedGroup / AddToFeedGroup / RemoveFromFeedGroup / Pin / Unpin。全部走 context ( noctx 修复 )。
- **`internal/chats/feishu/chat.go`** — Chat 契约的飞书实现：larkws 长连接 ( OnP2MessageReceiveV1 / OnP2CardActionTrigger / bot menu )、补收 ( state.feishuRead 水位 + ListMessages )、消息去重 ( ≤2000 ) + per-chat 串行队列、配对流程、租户标签 ( tagOf + Bind/Unbind + ended 标签 )、发帖管线 ( sendMarkdown 卡片→纯文本降级 )、/auth 命令、总览 ( overviewCard 原地编辑→重试 3 次→新发+撤旧 )、Ring ( @ 提及在静音群 )、MarkWorking/MarkDone ( THINKING→OK 表情 )。
- **`internal/chats/feishu/sdk.go`** — LarkAPI 接口的 SDK 实现 ( *lark.Client 包装：SendMessage/PatchMessage/ReplyMessage/RecallMessage/ListMessages/CreateChat/RenameChat/SetChatAvatar/UploadImage/UploadFile/DownloadImage/AddReaction/DeleteReaction/CreateTag/BindTag/UnbindTag )、总览卡片 ( overviewText 排序 + 状态彩签 + 群链接 )、cardActionToMap/messageReceiveToMap 事件适配。
- **依赖**：`github.com/larksuite/oapi-sdk-go/v3 v3.12.2` ( 唯一运行时新增依赖，spec §2 允许 )。

**留待 M6+ 的**：飞书端到端冒烟 ( 真实 app + 真实 hc )；SDK 的 CreateChat/CreateTag/BindTag 走 im.v2 原生请求 ( 目前返回占位错误 )；sips 头像仅 macOS ( Linux 跳过 )；`HERDR_SESSIONS` 多 session 的拓扑测试。

**已知测试抖动**：`TestLiveCardThenFinalAnswer` / `TestRestartContinuesSameLiveCard` 在全量并行 `-race` 下有 ~10% 概率因 Quick 定时器 ( 10ms ) 与断言时序竞态而超时 ( 生产代码无 race 检测器报告；两测试单跑全绿 )。已在 Quick 模式从 1ms→10ms、waitFor 5s→8s 后将抖动率从 ~30% 压到 ~10%。根治需要给测试一个暂停引擎定时器的钩子 ( M6 顺手做 )。

## M6 CLI 全命令 + 服务 + hooks 安装器 落了什么（2026-11-14）

代码约 21.4k 行（净增 ~5.0k），`make lint` 0 issues，`go test -race -count=1 ./...` 14 包连续多轮全绿（含此前抖动的两测试——本里程碑根治，见下）：

- **`internal/hooks`** — PermissionRequest hook 安装器（spec §8.3，移植 hooks.mjs）：`PlacesOf`（CLAUDE_CONFIG_DIR/CODEX_HOME/XDG_DATA_HOME）、`InstallHooks`（ask.sh 内嵌自 assets/hooks/ask.sh 逐字节复用、mode 0700；条目只增删含 `herdrchat/hooks/ask.sh` 标记的、装两遍仍只一条；首改自动备份 `.herdrchat-backup`；坏 JSON 绝不写；原子写保持原 mode；agent 没装的目录不碰）、`UninstallHooks`（摘干净、空数组/空 hooks 对象一并删回原样）、`StatusOf`（there/in 两态）。测试转录 hooks.test.mjs 安装器一节 + ask.sh 与 assets 逐字节一致断言。
- **`internal/service`** — launchd/systemd 服务（spec §10，移植 service.mjs）：`LaunchdPlist`（Label `app.herdrchat.<id>`、全路径、RunAtLoad/KeepAlive/ThrottleInterval 10、XML 转义、StandardOut/Error→service.out.log）、`SystemdUnit`（ExecStart 双引号、Environment 逐条引号与 `%`/`$`/`\` 转义、Restart=always/RestartSec=5/WantedBy=default.target）、`Of`（darwin→`~/Library/LaunchAgents/…plist`、linux→`$XDG_CONFIG_HOME|~/.config/systemd/user/herdrchat-<id>.service`、其余平台 nil）、`Installed/Write/Remove/BinGone`（Go 版天然没有 node 路径问题，BinGone 查二进制挪走）、Start/Stop/Disable/Running（launchctl bootstrap/bootout/print、systemctl --user daemon-reload/enable/restart/disable --now/show；Runner 可注入）。快照测试转录 service.test.mjs + launchctl/systemctl 调用序列逐条断言。
- **配对（spec §6.10，补 M2 欠账）** — `core/pairing.go`（6 位码 crypto/rand、15min/5 次）+ `engine/pairing.go`（tryPairing：无 owner 时直聊只认码；对不上计数、5 次销毁；码对→`SetOwner` 写 config + 回复 + 2s 后 `Restart` 让位新 owner 实例；无码/过期回指引，含"自己的 id 手填"一行——chat 契约加 `OwnerKey()`，`Incoming` 加 `Sender`，飞书直聊带上 open_id）。3 个引擎测试：码对认主、错码计数销毁、无码指引且 `/new`/`/kill` 对陌生人一概不跑。
- **`internal/cli`** — 全部子命令（spec §10）：
  - `run`：真接线（hccli + 共享 asks + claude.New/codex.New/opencode.New + feishu.Chat + engine）；xlog 按天落盘 `logs/<YYYY-MM-DD>.log`、14 天修剪；崩溃路径：主 goroutine panic→🔴 down 卡（≤300 字），重启后以心跳文件 `state/running` 判"上次没退干净"→✅ back 卡（SIGKILL/OOM 也兜得住）；SIGTERM/SIGINT 优雅排空。
  - `start`：readyConfig + 配置的 session 必须在本机；先停干净（防双 bot 双答）；服务形态走服务，否则首个 session 的 `hc run --bg` 终端放 while 循环脚本（变量白名单同服务、PATH 用终端自己的）；waitConnected 60s 读当日日志等 `feishu connected`，超时打最近 5 条 ERROR、exit 1。
  - `stop`（从所有形态停下）/ `status`（路径、必填键 set|missing 不打秘密、每 session core up|down|不在本机、运行形态与双实例警告、/auth refreshUntil、hook 状态、当日 ERROR/WARN 计数）/ `logs`（--errors/-n/--day，行格式即 xlog）/ `init`（内嵌 config.env.example 逐字节复用 + 测试锁定；--chat 只认 feishu）/ `install`/`uninstall`（服务文件每次重写、env 白名单 HERDRCHAT_HC/HERDRCHAT_PROFILE/PATH/HERDRCHAT_HOME/XDG_*；linux 提示 enable-linger）/ `pair`（owner 已设提示、bot 未跑拒绝、写 state.pairing、打印指引、2s 轮询 config ≤15min、以 waitConnected 收尾）/ `setup`（TTY 门（`stty -a` 判真 tty，`</dev/null` 也诚实拒绝）、hc --version 在场检查、密钥 stty -echo 无回显 + tenant_access_token/internal 即时校验、列本机 session 挑选、start→控制台两步→pair→问装服务）/ `version`/usage/未知命令 exit 2。
  - 全局：SIGPIPE 忽略 + `cli.PipeSafe`（EPIPE 安静 exit 0，`logs | head` 无噪音）。依赖全部从 `App` 注入（env/home/TTY/时钟/HC/SvcOf/CheckKeys/Stty/ConnectWait），测试不碰真实 home。
- **CLI 测试**（§13.11,12）：--profile 两种形式与非法名 exit 2、usage/version/unknown、init（0600、不覆盖、拒 telegram）、logs 四态、hooks（状态/非 TTY 拒绝/--yes/问 y·n）、start 三种拒绝（缺键/ghost session/telegram）、pair（owner 已设、bot 没跑、成功路径全程注入）、status 输出与不打秘密、loopScript 白名单（无 CLAUDE/PATH）、waitConnected 超时报最近错误、EPIPE、config.env.example 逐字节、**wiring**（对等 wiring.test.mjs：`var _ core.HC = (*hccli.CLI)(nil)` + 三 reader CheckReader+Register + feishu CheckChat + 引擎装配 + 配对码可用）。
- **飞书 M5 遗留补齐**：`CreateChat`（im.v1 chat.create，UserIdType open_id，拉 owner 入群）、租户标签 im.v2（`tag.create` tag_type=tenant，重名回 duplicate_id 拿来即用；`biz_entity_tag_relation` create 绑/update 解绑）；sdkClient 带 owner（Chat.Start 注入）。**未经真实飞书企业验证**（编译期对着 SDK 形状写就，端到端冒烟在 M8）。
- **readers 构造器**：`claude.New(hc, shared)`/`codex.New(hc, shared)`/`opencode.New(hc)`（Files/Shell/Cache/Asks 接好；codex 提问状态机的表建好——空表上写会炸；opencode 的 GLM Fetch 默认真 HTTP，走环境代理）。`core.HC` 接口加 `HomeFile`（FakeHC 补实现）；引擎加 `SetAsks`（与 readers 共享同一份 partial/finished 记忆）与 `ManualTimers`。
- **测试抖动根治**：引擎加 `ManualTimers`（测试自己 tick，Quick 定时器不再与断言抢时序），harness 与重启用例全部启用——`TestRestartContinuesSameLiveCard` 连续 6 轮 -race 稳定，全量多轮 0 失败。

**留待 M7.5/M8 的**：`herdrchat update`（goreleaser 链路）；真实飞书 app + 真实 hc 的端到端冒烟（setup→pair→发消息→点按钮→服务安装）；THIRD-PARTY-NOTICES 与文档更新；租户标签对拍真实返回。

**工程约定增补（spec §11 偏差的延伸）**：bot 落盘日志用 `xlog.SetupDaily(dir, keep)`——行格式不变（xlog 单行紧凑），文件按天 `logs/<YYYY-MM-DD>.log`、14 天修剪；`GOAW_LOG_FILE` 单文件行为原样保留；`herdrchat logs --day` 因此有真语义。

## M7.5 `herdrchat update` 落了什么（2026-11-14）

代码约 22.4k 行（净增 ~1.0k），`make lint` 0 issues，`go test -race -count=1 ./...` 15 包两轮全绿。对应 FINAL_PLAN §3 上游 1.11 漂移项的 Go 重设计：

- **`internal/cliversion`** — goreleaser 经 `-X` 注入的 `Version`（单独成包，ldflags 有稳定落点）；`Newer` 按点分段数值比较（v 前缀剥掉、预发布同段即同版、解析不出一律当"不新"——宁可少提示不瞎升级）；`DevVersion` 判发布构建（App/引擎都用它，不只看包变量）。
- **goreleaser 链路** — `.goreleaser.yaml`（linux/darwin × amd64/arm64、CGO 关、`-s -w -X …cliversion.Version={{.Version}}`；tar.gz 名 `herdrchat_<v>_<Os>_<x86_64|aarch64>.tar.gz` + `herdrchat_<v>_checksums.txt`——这两个名字写进 update 命令，改要一起改）+ `.github/workflows/release.yml`（tag `v*` 触发，vet+test 过了才 release）。**已验证**：`goreleaser check` 通过；`release --snapshot --clean` 四平台产物齐全、`-X` 生效（快照二进制 `version` 报 `0.0.0-SNAPSHOT-<sha>`）。
- **`herdrchat update [--check]`**（internal/cli/update.go）— 查 latest（GitHub API，`HERDRCHAT_UPDATE_API/REPO` 可换、走环境代理）→ 下载本平台 tar.gz + checksums → sha256 对不上坚决不装 → 从 tar 解出本体 → 同目录临时文件 + rename 原子替换（跑着的旧进程用旧 inode 不受影响）→ 重启：服务形态 launchctl/systemctl 重启；hc 终端形态 SIGTERM 心跳文件里的 pid（run 的 `state/running` 现在带 pid，两分钟新鲜度防 pid 复用），终端循环 3s 后拉起新的 → waitConnected 收尾。dev 构建也允许换（明说 "dev build: bringing in the release anyway"）；已是最新就直说。
- **bot 内新版提示**（engine/update.go，语义对齐上游 1.11）— 起跑 30s 后问一次、每 24h 再问；有更新且没提示过（`state.updateNoticed`，可选字段、Node 版忽略无害）→ owner 一张 "📦 herdrchat x is out" 卡；只提示不自动升级；dev 构建/网络失败静默。
- **验证**：update 全流程在注入的 Latest/Download 上 8 个用例（--check 不动二进制、已最新、替换+mode 0755、篡改字节拒装、缺平台资产、pid SIGTERM 真进程、服务重启调用序列、资产名与模板一致）；引擎提示 2 个用例（同版一次、更大版本再提示；同版/更旧/问不出/dev 全安静）；**真实产物 E2E**——本地起一个 GitHub API 形状的 HTTP 服务，用 snapshot 二进制真跑 `update`：下载真 tar.gz、校验真 checksums、原子替换、换完的二进制照常 `version`。

**留待 M8 的**：真实打 tag 出 release 走一遍 CI（需要仓库写权限）；真实 GitHub 上 `update` 的最终对拍。其余按 M8 计划：端到端冒烟、对拍、THIRD-PARTY-NOTICES、文档与迁移说明。

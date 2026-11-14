# 进度（PROGRESS.md)

> 对照 [FINAL_PLAN.md](FINAL_PLAN.md) §4 的里程碑。每完成一项更新此表并在末尾追加一段"落了什么"。

| 里程碑 | 状态 | 出口标准 |
|---|---|---|
| M1 骨架 + 契约 + Fake 基建 | ✅ 完成（2026-11-14） | 契约测试绿（`go test -race ./...` 全绿，64 用例） |
| M2 引擎（房间/发帖/游标/活卡片/总览/入口） | ✅ 完成（2026-11-14） | 55 用例全绿（`go test -race ./...`）；对话框状态机按 FINAL_PLAN 归 M3 |
| M3 对话框三通路 + hook + 菜单解析 | ✅ 完成（2026-11-14） | 79 用例全绿（5 轮 -race 稳定）；语料 28 份 |
| M4 readers ×3 + meter/footer | ✅ 完成（2026-11-14） | 105 用例全绿（-race）；Item.Card、命令目录、GLM 限额已并入；**M5 Telegram 已取消（仅飞书）** | + Item.Card 卡片（§2.B）+ 命令目录（§2.A）+ GLM 限额 |
| M5 Telegram | ⬜ 未开始 | |
| M6 飞书 | ⬜ 未开始 | |
| M7 CLI 全命令 + 服务 + hooks 安装器 | ⬜ 未开始 | |
| M7.5 `herdrchat update`（goreleaser） | ⬜ 未开始 | |
| M8 端到端验收 + 文档 + 迁移说明 | ⬜ 未开始 | + THIRD-PARTY-NOTICES |

## 工程约定（用户设定，2026-11-14）

- **日志统一用 `internal/xlog`**（包名已从 `logging` 改为 `xlog`）：`xlog.For("<组件>").Error("消息", "key", value)`。入口 `cmd/herdrchat` 已接 `xlog.Setup()`（env：`GOAW_LOG_LEVEL/COLOR/FILE`）+ panic recover。
- **与 spec §11 的偏差**：原计划的独立 logging 包（按天文件、14 天修剪、⏎ 折叠、HandleCrash）已被移除，由 xlog 替代——单行紧凑格式、`GOAW_LOG_FILE` 单文件追加、组件列。M7 做 `herdrchat logs` 命令时按 xlog 的落盘形态实现。
- **lint 基线**：`make lint`（golangci-lint 基线档，CI 阻断）必须 0 issues，当前已达成；`make lint-strict` 为软门槛（现有 revive×50 / prealloc×2 报告，不阻断）。
- 仓库卫生：`.DS_Store` 已删并入 `.gitignore`；`assets/` 与 `testdata/node-tests/` 是 reuse.md 认定的复用资产，保留。

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

# herdrchat Go 重写优化建议（optimization.md)

> 面向 Go 重写的改进清单。分为：**A. 必须保持的不变量**（重写不可丢的语义）、**B. 架构级改进**（修正原版已知缺陷）、**C. 性能优化**、**D. 可靠性增强**、**E. 功能增强**。
> 与 `spec.md` 配合阅读：spec 描述"重写成什么样"，本文描述"在哪里可以做得比原版更好"。已纳入 spec 的优化（如 Telegram 分 chat 队列、优雅退出）在此说明动机。

---

## A. 必须保持的不变量（不可"优化"掉的东西）

重写时最容易丢的是语义细节，以下任何一条的丢失都会造成可感知的退化：

1. **"宁可重复、不可丢失"** 的投递语义：失败保留游标、活卡片编辑失败整卡重发、重启续编同一张卡；
2. **一题一卡** 与**对话框身份哈希**防陈旧应答（点击前必须重读屏幕复核）；
3. **lane 全序**：同终端副作用串行，否则状态文件与卡片会错乱；
4. **首启不补历史**（游标=启动时刻）与**新房间回填**的区分；
5. hc 身份判定用 **core id 而非名字**；断连机器**挂起而非判死**；
6. 非 owner 一律忽略；秘密不落日志；
7. 时间/大小常量（spec §15）是对平台实测限制的校准，未经实测不要"顺手调优"。

---

## B. 架构级改进

### B1. 拆解 engine 巨石（原版最大结构问题）

原 `engine.mjs` 1,479 行，混合房间/发帖/对话框/入口/总览五类职责，且通过闭包共享大量可变状态。Go 版按 spec §2 拆成 `rooms.go / posting.go / dialogs.go / intake.go / overview.go`，engine.go 只做接线。**关键要求：拆分后各子模块间不共享裸变量，状态显式经由 Engine 结构体传递**，否则只是把巨石文件切成互相纠缠的碎片。

### B2. 接口最小化（ISP）

原 Chat 契约约 30 个成员一次性实现门槛高。Go 版允许组合拆分：

```go
type Chat interface { Meta; Rooms; Poster; Attention; Media; Owner }  // 组合接口
```

测试可按子接口打桩；`checkChat` 改为按能力探测（缺核心能力报错，缺可选能力自动降级），新平台接入成本更低。

### B3. 单二进制消除"node 漂移"问题

原版服务文件写死 node 绝对路径，node 升级即失效（专门有 `nodeGone()` 检测+提示重装）。Go 静态编译后服务文件指向自包含二进制，**该问题整类消失**；顺带删掉 `status` 中的 nodeGone 分支与相关文档。

### B4. 配置热重载

原版改 config.env 必须 `herdrchat start`。Go 版可加 SIGHUP / 文件 mtime 监听：`HERDR_SESSIONS`、`HERDR_MUTED_SPACES` 等纯读取项热生效；owner/密钥变更仍提示重启（或自动重建 chat 连接）。低风险高体验。

### B5. 明确的进程生命周期

原版靠外层 `while true` 循环或系统服务拉起，自身无优雅退出。Go 版（spec §6.11）：
- SIGTERM/SIGINT → 停定时器 → 排空 lanes（上限 5s）→ 落盘 state → 退出；
- 服务文件的 `Restart=always` 变为真正的崩溃兜底而非常规重启手段；
- system unit 加 `TimeoutStopSec` 与 kill 顺序。

---

## C. 性能优化

### C1. Telegram：全局单队列 → 分 chat 队列 + 全局令牌桶（已纳入 spec）

Telegram 限制是**每 chat 约 1 msg/s、全局约 30 msg/s**。原版全局 1.05s 串行队列在多终端并发时是人为瓶颈（几十个 topic 时新消息明显排队）。改为：
- 每 chat_id 一条 1.05s 队列（多终端并行送达）；
- 全局 30/s 令牌桶总闸；
- 429 `retry_after` 照旧自适应。
预期多终端场景吞吐提升一个数量级。

### C2. hc 子进程调用量削减

原版每 5s live 扫描对每个 working 终端执行多次 `hc`（read 尾屏 + get），60s sweep 再来一轮；终端多时进程创建开销可观。Go 版：
- **事件侧已有终端记录**的（status/labels/title），不再额外 `hc get` 复核（原版多处防御性 get 可借 lane 语义与事件信任度收敛）；
- `ScreenTail` 结果按 `(address, 500ms)` 去重共享（同一轮多调用者合并成一次子进程）；
- 远端机器的 `hc exec` 批量化：把同一机器同一轮的多个 shell 片段合并成一次 `sh -c 'a; b; c'`（asks 扫描 + meter + 分支探测天然可合并）；
- 更进一步（可选，M8 后）：`hc exec` 常驻化——起一个远端 `sh` 会话复用 stdin/stdout，消灭逐次冷启动。需与 hc 团队确认协议稳定性，作为实验特性。

### C3. state.json 写放大

原版每次标签/回执/游标更新都整文件读-改-写。终端与房间多时（百级）写放大明显。Go 版两档：
- **默认保持 JSON**（兼容承诺），但改为**合并写**：dirty 标记 + 250ms 合并刷盘 + 退出强制刷；读侧全量缓存（lane 串行保证一致视图）；
- **可选 bbolt/SQLite 后端**（`HERDRCHAT_STATE=sqlite`），同 schema 映射，KV 按 key 读写；提供 `herdrchat migrate state` 双向转换。新装用户默认 sqlite，老用户 JSON 直至主动迁移。

### C4. 游标 `seen` 有界化

原版 seen 列表靠 `at > cursor.at` 自然收缩，理论上 10 分钟窗口内高频会话可积累大量 id。Go 版用环形切片/布隆（误杀率极低时退化为整文件重读，安全），或简单 cap 10k 条目（超出触发一次全量重读校准）。正确性不变，内存可预测。

### C5. 图片上传并行化

原版 `uploadImages` 串行逐张。Go 版同 room 内并发上传（飞书限流用信号量 5），失败语义不变（3 次上限、逐图独立降级）。

### C6. 事件解析零拷贝倾向

`hc events --json` 快照行可达 MB 级。用 `json.Decoder` 流式逐行、复用 `json.RawMessage` 延迟解析（只解用到的字段），Scanner 缓冲 10MB（原版 readline 无上限是隐患，Go 版显式上限 + 超限截断告警）。

---

## D. 可靠性增强

### D1. 结构化可观测性

- `log/slog` 全结构化（terminal/session/room/latency 字段化），保留人类可读文本格式兼容 `herdrchat logs`；
- 新增 `herdrchat status --json`（机器可读：lane 积压深度、最后事件 seq、平台连接状态、重试计数）——skill 与监控系统直接消费；
- 可选 pprof（`HERDRCHAT_PPROF=127.0.0.1:0`）便于现场诊断。

### D2. 重试预算与断路器

原版发帖 5 次固定退避、装饰类无限静默失败。Go 版：
- 平台 API 连续失败 N 次（如 20 次/5 分钟）→ 断路：暂停该平台发送、每 60s 探活、恢复后从游标处继续（语义仍是"宁重不丢"）；
- owner 通知通道独立小队列，断路期间 down/back 通知延后补发。

### D3. 时钟与单调性

跨机器日志时间戳比较（cursor `at`）用墙上钟，NTP 跳变可能造成漏读（`at` 突然超前）。Go 版：检测 entry 时间戳回拨 >LAG_MS 时强制该终端一次全量重读校准（幂等，seen 去重保证不重发）。

### D4. 崩溃面收敛

- Go panic 在 lane 内 recover（原版靠进程重启）——recover 后该 lane 重建、其余终端不受牵连；连续 panic（>3 次/分钟）仍自杀重启，防半死状态；
- `state.json` 损坏（JSON 坏/半写）：原版静默从空状态重启（**会丢所有房间映射！**）。Go 版：检测到损坏先备份 `state.json.corrupt-<ts>` 再用空状态起，并 owner 通知"状态损坏已重置，旧房间将重建"——把无声数据丢失变成有声恢复。

### D5. 双实例互斥

原版靠 `start` 时 kill 旧终端防双 bot。Go 版加状态目录 `flock`（跨进程），第二个实例直接退出并提示——覆盖手工误启、服务+终端双开等所有路径。

### D6. 集成测试：脚本化假 hc

把 FakeHC 做成**独立可执行文件**（Go test helper `testdata/fakehc`），按剧本文件回放事件/响应，供引擎层黑盒测试与 Go 版/Node 版**对拍**（同剧本两侧 FakeChat 调用序列 diff）——这是重写等价性的最强验证手段。

---

## E. 功能增强（重写后顺势补齐，均不破坏契约）

| 项 | 说明 | 优先级 |
|---|---|---|
| Lark（国际版飞书） | 域名 open.lk.larksuite.com；SDK 同源，config 加 `FEISHU_BASE` 即可 | 高（中文社区外用户强需求） |
| 多 owner | `FEISHU_OWNER_OPEN_ID` 支持逗号列表；配对流程不变（首个配对者为主） | 中 |
| 消息编辑/撤回感知 | 飞书 message 回调里 edited 标记 → 提示"对方改了刚发的话"（现版本忽略） | 低 |
| `/new` 模板 | config 可定义命名命令（`HERDR_NEW_TEMPLATES=review:claude --continue …`） | 低 |
| Slack / Discord 插件 | 契约已预留；Slack Bolt socket mode / Discord gateway 均为出站连接，契合零暴露架构 | 中 |
| Web dashboards 只读链接 | 总览定期快照为静态 JSON（不引入服务器，仅本地文件+用户自行托管） | 低 |
| Codex 定位增强 | 见下 | 中 |

### Codex 日志定位增强（修正原版已知启发式缺陷）

原版"同 cwd 同刻启动的两个 Codex 可能错配"。Go 版叠加两个信号：
1. `hc exec` 读 `/proc/<pid>/cmdline`（Linux）/`ps -o command`（macOS）：Codex CLI 常把 session/rollout 线索带在 argv 或环境（`CODEX_…`）里，逐版本探测可用则优先；
2. 请求文件名匹配兜底（现 `strayAsk.isOf` 逻辑前移为定位手段之一）；
3. 仍歧义时：两个候选都在写 → 选写更新者并在卡片 footer 标 `⚠ log match ambiguous`，提示用户确认——**把静默错配变成显性警告**。

---

## F. 明确不做 / 不急

- **不改 hook 协议**（ask.sh 字节级复用，双语言版本共存期互操作靠它）；
- **不引入 ORM/框架**（net/http + 标准库足够；依赖越多越伤这个"单二进制常驻服务"的定位）；
- 不做 GUI、不做入站 webhook 模式（长连接/长轮询的零暴露架构是产品根特性）；
- 暂不做水平扩展（单 owner 单机是安全模型的根基）。

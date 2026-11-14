# 源项目可复用资产清单（reuse.md）

> 盘点 Node.js 原版（v1.10.2）中 **Go 重写可直接/经移植复用** 的资产。
> **①②④ 类资产已全部复制进本目录**（见下"目录地图"），Go 重写时直接从 `assets/`、`testdata/` 取用；③ 类为源仓库中的知识，需对照原文件翻译。
> 分级说明：
> - **① 原样复用**：文件直接拷贝，零改动或仅路径调整 → 已复制
> - **② 协议/格式复用**：保持不变，Go 侧按其实现（兼容性的根基）→ 规格见 spec.md
> - **③ 知识移植**：算法/正则/模板照抄改写为 Go → 留在源仓库，对照翻译
> - **④ 素材复用**：golden 数据、文档、CI 脚本 → 已复制
> - **✗ 不复用**：JS 生态绑定，须重写

---

## 目录地图（已复制内容）

```
go-rewrite/
├── README.md / spec.md / optimization.md / reuse.md     本资料包
├── assets/                         ← ① 原样复用 + ④ 文档/CI 素材
│   ├── hooks/ask.sh                PermissionRequest hook（逐字节复用，双版本共存期的互操作桥）
│   ├── skills/herdrchat/           Skill 包（SKILL.md + references/{feishu,herdr,ops,telegram}.md）
│   ├── scripts/smoke.sh            CI 冒烟脚本（npm pack/install 换 go build 即可）
│   ├── scripts/wait-release.sh     发布等待脚本
│   ├── docs/README.md              英文 README（更新安装段后即用）
│   ├── docs/README.zh.md           中文 README（同上）
│   ├── docs/guide.md               用户指南（419 行；行为描述基本仍准确，仅安装/服务/node 段需更新）
│   ├── docs/architecture.md        架构文档（模块树需按 Go 结构重画）
│   ├── ci/ci.yml                   测试矩阵 workflow（Node 版本换 Go 版本）
│   ├── ci/published.yml            发布后 workflow
│   ├── ci/release.yml              发布 workflow
│   ├── config.env.example          示例配置（键表不变）
│   ├── CHANGELOG.md                历史版本记录（保留，Go 版续写）
│   ├── LICENSE                     MIT
│   └── .releaserc.json             semantic-release 配置（commit 规范沿用；npm 插件换 Go 发布方式）
└── testdata/
    └── node-tests/                 ← ④ 原版全部 23 个测试文件（含 fakes.mjs）
        ├── fakes.mjs               FakeChat/FakeHC/FakeReader 行为规格 —— Go 测试桩的蓝本
        ├── engine.test.mjs         内核行为 ground truth（契约校验也在其中）
        ├── readers.test.mjs        Claude transcript 样本 + 期望 items（golden）
        ├── codex.test.mjs          Codex rollout 样本 + 期望（含日志定位 pickLog）
        ├── codex-questions.test.mjs Codex 两类提问的完整状态机用例
        ├── opencode.test.mjs       OpenCode API 响应形状 + 期望 items
        ├── menu.test.mjs           屏幕菜单解析用例（边框/无框/多选/free/beside）
        ├── screen.test.mjs         屏幕清洗用例
        ├── cursor.test.mjs         游标算法用例（同时间戳/晚写/收缩）
        ├── views.test.mjs          多视图拓扑用例
        ├── feishu.test.mjs / telegram-format.test.mjs   平台转换 golden
        ├── service.test.mjs        launchd/systemd 文件快照
        ├── hooks.test.mjs          hook 安装/卸载（含备份与坏 JSON 拒写）
        └── （其余 config/hc/log/meter/overview/userauth/skill/wiring/docs）
```

> **注意**：`testdata/node-tests/` 里的样本是测试内联构造的（helper 函数拼 JSON 行），**断言即期望输出** —— 移植时整份对照翻译比抽取裸 JSONL 更可靠；如需独立 `.jsonl` golden 文件，可在 Go 侧把各 `const lines = [...]` 展开落盘。

---

## ① 原样复用（已复制到 `assets/`）

| 资产 | 原位置 | 现位置 | 说明 |
|---|---|---|---|
| **PermissionRequest hook 脚本** | `lib/hooks/ask.sh` | `assets/hooks/ask.sh` | 纯 POSIX sh，与运行时语言无关。**逐字节复用**；安装器把它拷到 `~/.local/share/herdrchat/hooks/ask.sh` 的行为也照旧。新旧版本共存期靠它互操作 |
| **Skill 包** | `skills/herdrchat/` | `assets/skills/herdrchat/` | 教 coding agent 运维 herdrchat 的技能。命令名/路径全部不变 → 原样可用；仅需核对个别提到 "node" 的措辞 |
| **config.env.example** | 根目录 | `assets/config.env.example` | 键表与注释不变（spec §4.2 有全表） |
| **LICENSE** | 根目录 | `assets/LICENSE` | MIT |
| **CHANGELOG.md** | 根目录 | `assets/CHANGELOG.md` | 历史保留；Go 版从新起点续写 |
| **CI 冒烟脚本** | `scripts/smoke.sh` | `assets/scripts/smoke.sh` | "打包→全局安装→装 skill→跑首步"流程；把 `npm pack/install` 换成 `go build` 产物安装即可 |
| **发布等待脚本** | `scripts/wait-release.sh` | `assets/scripts/wait-release.sh` | 同上微调 |

## ② 协议/格式复用（Go 侧实现必须逐字段对齐，规格见 spec.md）

| 协议/格式 | 源位置 | 兼容性意义 |
|---|---|---|
| **hook asks 协议** | `lib/core/asks.mjs` + `assets/hooks/ask.sh` | `~/.local/state/herdrchat/asks/{req,ans}/<epoch>-<pid>.json`、`{"agent","terminal","at","request"}`、空 ans=无决策、24h 清理、`herdrchat.ask` 标签格式 `^\d+-\d+$`。**一字不改** |
| **state.json schema** | `lib/core/config.mjs`（readState/writeState）+ engine 各处写入 | 字段名见 spec §4.3。保持兼容 = 老用户原地换二进制无损接管。注意 JSON 数字/字符串类型混用照抄原字段类型（如 `reacts.at` 为毫秒 int、游标 `at` 为 ISO 字符串） |
| **config.env 格式** | `lib/core/config.mjs`（parseEnv/setConfigValue） | 逐行 KEY=value、引号剥离、环境变量优先、增量改写保留注释 |
| **目录布局与 profile id 规则** | `lib/core/config.mjs`（placesOf） | XDG 路径、HERDRCHAT_HOME、移动目录加 `-<sha1[:6]>` 后缀 |
| **服务文件语义** | `lib/core/service.mjs` | Label 命名 `app.herdrchat.<id>`、KeepAlive/ThrottleInterval 10s、systemd Restart=5s、env 白名单、service.out.log 位置。老用户升级时不重装服务也兼容 |
| **hooks 安装的文件契约** | `lib/core/hooks.mjs` | 条目形状（command/timeout/statusMessage）、`herdrchat/hooks/ask.sh` 标记、`.herdrchat-backup` 备份、1790/590 与 1800/600 秒参数 |
| **CLI 命令面** | `bin/cli.mjs` usage | 子命令名、`--profile/--chat/--day/-n/--errors/--yes`、退出码（usage=2、运行错=1）、EPIPE 行为。skill 与用户肌肉记忆依赖它 |
| **日志行格式** | `lib/core/log.mjs` | `<UTC ISO> <LEVEL> <text>`、按本地日期命名、14 天保留、` ⏎ ` 折叠；`herdrchat logs` 与 `waitConnected`（等 `[feishu|telegram] connected`）依赖此格式 |

## ③ 知识移植（算法/正则/模板 → Go 照抄改写；对照源仓库原文件）

| 知识 | 源位置 | 移植要点 | 对拍用例（本目录） |
|---|---|---|---|
| **游标算法** | `lib/core/cursor.mjs` | LAG_MS=10min、seen 窗口、id 生成（uuid 或 sha1 特征串、`#i` 后缀）、complete 判定、advance 收缩。~60 行核心，逻辑闭合，逐条翻译 | `testdata/node-tests/cursor.test.mjs` |
| **屏幕菜单解析** | `lib/core/menu.mjs` | 全部正则（spec §6.5.1 已照抄）：OPTION/CURSOR/RULE/BORDER/FREE/BESIDE/LEFT_BORDER/MARKS、连续编号段、top 定位、抗 resize 哈希。**这是逆向终端 UI 的核心资产，勿重造** | `menu.test.mjs`、`screen.test.mjs` |
| **Claude transcript 解析规则** | `lib/core/readers/claude.mjs` | sessions/<pid>.json 定位、projects 路径编码（非字母数字→`-`）、narration 判定（base64 signature 含 "narration"）、queued_command/AskUserQuestion answers/toolUseResult、usage 口径（input+cache_read+cache_creation）、synthetic model 剔除、toolLine 格式、`/context` 与 `/usage` 解析正则 | `readers.test.mjs` |
| **Codex rollout 解析规则** | `lib/core/readers/codex.mjs` | find+首行 cwd/timestamp 的定位脚本、pickLog 的 `started-5s` 规则、entryItems 全 switch、token_count→limits 映射、Plan/async 两类提问状态机（questionsIn/questionOnScreen/answerPlan 按键序列）、laterMessage 多题格式、titleOf 清洗 | `codex.test.mjs`、`codex-questions.test.mjs` |
| **OpenCode API 形状** | `lib/core/readers/opencode.mjs` | `__herdrchat_exit=$?` 成功判定技巧、pickSession（active→busy→updated）、message.list 翻页与 End 判定、form/permission 字段与应答、model.list 的 limit.context | `opencode.test.mjs` |
| **hc CLI 全知识** | `lib/core/hc.mjs` + `events.mjs` + `home.mjs` | 命令/flags/JSON 形状/错误 kind（finished/unavailable）、`--` 防 `-` 开头误判、事件 JSON-RPC 序列与 seq 去重、HC_TERMINAL_* 剔除防 `<hc from>` 包裹、core.lock socket 探活、sessionsHere 只读目录不询问、nameTab 序列、plainError/tidyScreen/machineOf | `hc.test.mjs` |
| **多视图拓扑** | `lib/core/views.mjs` | 身份=`core id|tN`、owner 优先级（直接 session→config 序首连）、owns 三值回退、reachable。~60 行直接翻译 | `views.test.mjs` |
| **meter 缓存** | `lib/core/meter.mjs` | lastKnown/knownSoon 语义（过期后台刷、首次限时等）、tokens()/windowName()/footerWords 格式 | `meter.test.mjs` |
| **engine 行为语义** | `lib/core/engine.mjs` | 全部流程已固化在 spec §6；实现时对照原文件逐段翻译比凭 spec 重想要快（尤其对话框状态机边角：again/own/idle/WAIT 分支、CLICK_BUDGET 慢机回执、卡片刷新而非新发） | `engine.test.mjs` + `fakes.mjs` |
| **飞书卡片 JSON 模板** | `lib/chats/feishu/client.mjs` | schema 2.0 全部模板：textCard（update_multi）/menuCard（checker+form+Next+free input+Esc，短选项 flow 布局 ≤4×14 字符）/doneCard/foldedCard（collapsible_panel）/footer HTML 实体转义/chunk 围栏感知切分（6000）/TONES 色彩映射/tagName 16 字符在 `-_. ` 处截断 | `feishu.test.mjs` |
| **飞书平台经验** | `lib/chats/feishu/*` | 3 秒回调时限→先答后办、去重+分 chat 队列、feishuRead 水位+message.list 补收、标签 5/s→250ms 队列、卡片内 @ 不通知→文本回复 @、/auth 设备流与 5 个 scope、sips 头像仅 macOS、文件 30MB | `feishu.test.mjs`、`userauth.test.mjs` |
| **Telegram 实现经验** | `lib/chats/telegram/*` | 限速（1/s per chat、429 retry_after）、callback_data 64 字节→id 间接表（LRU 300）、4096 切分、HTML 失败降级纯文本、可展开 blockquote 不嵌套代码、largestPhoto、`/cmd@bot` 剥离、服务消息跳过、offset 逐条确认持久化、COMMANDS 表、6 色图标分配 | `telegram-format.test.mjs` |
| **服务文件模板** | `lib/core/service.mjs` | plist 与 unit 的转义函数和字段全表，近乎逐行翻译（仅 ProgramArguments 指向 Go 二进制） | `service.test.mjs` |
| **CLI 交互细节** | `bin/cli.mjs` | setup 向导的步骤序与话术、askSecret 无回显 raw-mode、waitConnected 轮询日志、stopEverywhere 防双实例、status 各行文案、pair 的 2s 轮询与超时提示 | — |
| **配对/崩溃通知语义** | `pairing.mjs` + `bin/herdrchat.mjs` | 6 位/15min/5 次、claimOwner 写 config 后 2s 退出、down/back 卡片与 10min 限频 | `engine.test.mjs`、`wiring.test.mjs` |

## ④ 素材复用（已复制）

| 素材 | 原位置 | 现位置 | 用法 |
|---|---|---|---|
| **原版全部测试**（样本+断言=golden） | `test/*.mjs` | `testdata/node-tests/` | Go 表驱动测试逐用例转录；断言即期望输出。这是**重写等价性回归的锚点** |
| **Fake 三件套规格** | `test/fakes.mjs` | `testdata/node-tests/fakes.mjs` | FakeChat（记录全部调用）/FakeHC/FakeReader 的行为规格 → Go 测试桩蓝本 |
| **用户文档** | `README*.md`、`docs/*.md` | `assets/docs/` | 90% 原样保留。需更新：安装（npm→发布物）、"Running as a service" 的 node 段、architecture 模块树、limits 一节（Codex 定位、TG 限速若已优化） |
| **CI workflows** | `.github/workflows/*.yml` | `assets/ci/` | `ci.yml` 矩阵换 Go 版本（如 1.22/1.23/1.24），冒烟 job 换 `go build`；release/published 按新发布物调整 |
| **发布配置** | `.releaserc.json` | `assets/.releaserc.json` | commit 规范与 changangelog 插件沿用；npm/g 插件换成 Go 发布（goreleaser 等） |
| **scope 清单** | `skills/herdrchat/references/feishu.md` | `assets/skills/herdrchat/references/feishu.md` | 飞书权限 JSON（批量导入用），原样 |
| **常量表** | 源码各处 | spec §15 已汇总 | 全部时间/大小/限流常量从源码逐个抄录，直接引用 |

## ✗ 不复用（JS 绑定，Go 重写）

| 项 | 原因 |
|---|---|
| `@larksuiteoapi/node-sdk` 依赖与调用代码 | 换 `github.com/larksuite/oapi-sdk-go/v3`（官方，含 larkws 长连接，事件注册模型类似） |
| Telegram `fetch` 调用代码 | 换 `net/http`（行为规格在③，代码重写很薄） |
| `bin/herdrchat.mjs` 组装胶水 | 换 `internal/` 依赖注入结构 |
| node --test 测试**本体**（其**用例语义**已保留在 `testdata/`） | 换 go test 表驱动转录 |
| semantic-release 的 npm 发布链 | Go 用 goreleaser / goreleaser+semantic-release 组合；commit 规范沿用 |
| JS 的 readline 事件流读取 | 换 bufio.Scanner（注意 ≥10MB 缓冲，原版无上限是隐性差异） |

---

## 复用优先级建议（落地顺序）

1. **已就绪、零成本**：`assets/hooks/ask.sh`、`assets/skills/`、`assets/config.env.example`、`assets/LICENSE` —— Go 仓库第一天直接引用；
2. **第一批移植 + golden 锁死**：cursor / menu / views / meter（纯函数）—— 对照 `lib/core/*.mjs` 翻译，用 `testdata/node-tests/{cursor,menu,screen,views,meter}.test.mjs` 的用例跑等价；
3. **对照原文件翻译**：三个 reader（`testdata/node-tests/{readers,codex,codex-questions,opencode}.test.mjs` 全绿）→ hccli/events（`hc.test.mjs`）→ engine 各子模块（`engine.test.mjs` + `fakes.mjs` 桩）；
4. **平台两件套**：Telegram（纯 stdlib 先打通，`telegram-format.test.mjs`）→ 飞书（SDK + `feishu.test.mjs` 卡片模板移植）；
5. **外围**：service（`service.test.mjs` 快照）、hooks 安装器（`hooks.test.mjs`）、CLI 向导、日志 capture（`log.test.mjs`）；
6. **文档收尾**：以 `assets/docs/` 为底本更新 README×2、guide、architecture。

> 经验法则：**凡"协议"与"格式"一律照抄（②），凡"算法"一律翻译 + 用 `testdata/node-tests/` 的断言对拍（③），凡"代码组织"按 Go 习惯重构（不复用）**。重写等价性的信心来自 golden 对拍，而非代码相似度。

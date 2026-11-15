# 重写工程总结（Node → Go）

> 本文是六份规划与过程文档的精炼：**PROJECT_ANALYSIS.zh.md**（原版分析）、**spec.md**
> （实现规格书）、**optimization.md**（优化建议）、**reuse.md**（可复用资产清单）、
> **FINAL_PLAN.md**（执行总纲）、**PROGRESS.md**（里程碑日志）。
> 原文已从仓库移除，全文见 git 历史（`v1.0.1` tag 及更早提交）。

## 背景与目标

把 Node.js 版 herdrchat（上游 [zhangxiangliang/herdrchat](https://github.com/zhangxiangliang/herdrchat)
v1.10.2）重写为 **Go 单二进制**，要求：

1. **功能完全对等**：房间镜像、活卡片、对话框三通路、输入双向、总览、CLI、服务管理、
   hook 安装器——行为规格全部复述进 spec，不依赖读源码；
2. **数据与协议兼容（原地升级）**：`config.env`、`state.json`、asks 目录与
   `herdrchat.ask` 标签、`ask.sh`（逐字节）全部不变——Node 版用户停旧进程、放入新
   二进制、`herdrchat start` 即完成迁移，状态无损接管；双版本共存期可互操作；
3. **修正原版已知缺陷**（optimization.md 的 B/D 项），并吸收上游 1.11–1.12 的协议级
   变化（update 自更新、GLM 限额），不追新功能。

产品根特性不变：**出站长连接/长轮询，零入站暴露**——不需要公网 IP、VPN 或开端口。

## 基线决策（FINAL_PLAN §0）

| 决策点 | 结论 |
|---|---|
| 网络模型 | 主机常驻进程 + 出站长连（飞书 larkws） |
| 语言与形态 | Go 单二进制 `herdrchat`（macOS + Linux，x86_64/aarch64） |
| 兼容承诺 | spec §1 硬性要求全部保留 |
| 范围裁剪 | **Telegram 取消**（M4 用户决策，仅飞书；state.json 的 TG 字段保留兼容）；Lark、Slack/Discord 架构预留不实现 |
| 上游基线 | 行为冻结在 v1.10.2；1.11（update）重设计为 goreleaser 链路（M7.5）；1.12（GLM 限额）并入 opencode meter（M4） |
| 依赖政策 | 运行时唯一外部依赖：飞书官方 SDK；其余全标准库 |
| cobanov 增强项 | 仅采纳知识级移植（命令目录/结构化条目与子代理卡/屏幕语料/能力门控/错误分类学），不引入其 SSH/PWA/推送中继 |

## 复用分级（reuse.md 的结论）

- **① 原样复用**：`ask.sh`（PermissionRequest hook，逐字节，现内嵌于 `internal/hooks/`）、
  skills 包、示例配置（现内嵌 `config.env.example`）；
- **② 协议/格式复用**：asks hook 协议、`state.json` schema、`config.env` 键表、
  launchd/systemd 服务语义——Go 侧按原样实现，兼容性的根基；
- **③ 知识移植**：游标算法（LAG 10 分钟、seen 收缩）、菜单解析正则（menu.mjs 逐条
  照抄）、三个 reader 的日志解析规则、平台限速与卡片模板经验；
- **④ 素材复用**：原版全部测试转录为 Go 测试（断言即 golden）；真实抓屏语料 28 份
  （借自 [cobanov/herdrchat](https://github.com/cobanov/herdrchat)，Apache-2.0，归属见
  [THIRD-PARTY-NOTICES](../THIRD-PARTY-NOTICES)）；
- **✗ 不复用**：JS 生态绑定的一切（npm 打包/CI、readline、闭包式引擎）。

Go-only 整洁化后，Node 资产全部退场：`testdata/node-tests/` 与 npm 打包/CI 移除，
hook 脚本与示例配置以**包内嵌副本为唯一正本**。

## 增强项落地情况

原版分析发现的主要缺陷 → 重写的处置（如实标注）：

| 原版问题 | 处置 | 状态 |
|---|---|---|
| `engine.mjs` 1,479 行巨石、闭包共享状态 | 拆 `rooms/posting/dialogs/intake/overview`，engine.go 只做接线，状态经 Engine 显式传递 | ✅ |
| 服务文件写死 node 路径，升级即失效（nodeGone） | 单二进制，该问题整类消失；`BinGone` 只查二进制被挪走 | ✅ |
| `state.json` 损坏静默从空状态重启（丢全部房间映射） | 备份为 `state.json.corrupt-<ts>` + 有声报错 | ✅ |
| 事件流 readline 无上限（快照行可达 MB 级） | Scanner 显式 10MB 上限 | ✅ |
| 进程无优雅退出 | SIGTERM/SIGINT → 停定时器 → 排空 lanes ≤5s → 退出 | ✅ |
| panic 只能靠进程重启 | lane 内 recover；连续 panic 自杀重启（见 `internal/core/lanes.go`） | ✅ |
| Telegram 全局单队列瓶颈 | 随 Telegram 取消不再适用 | ➖ |
| 配置热重载、断路器、state 后端可选 sqlite、`status --json`、图片并行上传、hc 调用批量化 | 未做（重写以对等为先，这些留在 backlog） | ⬜ |
| Chat 契约 29 成员实现门槛高（建议拆子接口） | 实际保留单一接口，用**编译期检查**替代 Node 的运行期成员探测；`CheckChat` 保留合理性校验 | 部分 |

cobanov 知识级增强（全部落地）：**2.A** 斜杠命令目录（`/commands` 卡，M4）；**2.B**
`Item.Card` 子代理/工作流卡与紧凑工具行（M4）；**2.C** 屏幕语料库 28 份锁死菜单解析
（M3）；**2.D** hc 能力探测（失败按最低能力降级，绝不拒启，M1）；**2.E** 错误分类学
（哨兵错误 + `/herdrchat fix` 分诊表，M1/M8）。

## 里程碑与执行结果

全部完成（2026-11-14 至 11-15，两天）。各里程碑的代码细节记录同样见 git 历史中的
PROGRESS.md。

| 里程碑 | 内容 | 出口标准 |
|---|---|---|
| M1 | 骨架 + 契约 + Fake 基建 | 契约测试绿（`go test -race`） |
| M2 | 引擎：房间/发帖/游标/活卡片/总览/入口 | 引擎行为测试全绿 |
| M3 | 对话框三通路 + hook + 菜单解析 + asks | golden 全绿；语料 28 份 |
| M4 | readers ×3 + meter/footer + 命令目录 + 子代理卡 | golden 全绿；**Telegram 于此取消** |
| M5 | 飞书（larkws、卡片 schema 2.0、/auth 设备流、补收） | 平台测试全绿 |
| M6 | CLI 全命令 + 服务 + hooks 安装器 + 配对 | 服务快照/安装器/CLI 测试全绿 |
| M7.5 | `herdrchat update`（goreleaser 发布链路 + 新版提示） | 真实产物本地 E2E 对拍通过 |
| M8 | 端到端验收 + 文档 + 迁移说明 + THIRD-PARTY-NOTICES | 冒烟全链路（见下） |

**M8 冒烟记录**（真实 hc + 真实飞书 + 真实 GitHub，2026-11-15）：`herdrchat install`
systemd 服务化并 connected；tag `v1.0.0`/`v1.0.1` → CI（vet+race 过才放行）→ release
四平台产物；`herdrchat update` 两次（dev→1.0.0、1.0.0→1.0.1：查 latest → 下载 +
sha256 校验 → 原子替换 → 服务热重启 → connected）。顺带验证了下载故障路径（CDN 超时
→ 干净报错、二进制未动）。CI lint 的坑：golangci-lint 与分析用 Go 版本不匹配会
typecheck 硬失败（exit 3），已在 ci.yml 成对钉版（v2.15.0 × go1.27.3）。

**工程约定**（执行期确立）：日志统一 `internal/xlog`（替代 spec §11 原计划的独立
logging 包——单行紧凑、按天文件、14 天保留）；`make lint`（golangci-lint 基线档）0
issues 是 CI 阻断线；测试用 `-race -count=1 ./...` 全绿为合并前提；引擎提供
`ManualTimers` 让测试自驱定时器。

## 后续 backlog

- FINAL_PLAN §2.6：**F** Claude 的 `Dialog()` 在 hook 缺席时从 transcript 解析 blocked
  弹窗（把读屏再往后挤）；**G** `hc terminal attach` 流式屏幕（依赖能力门控，替代
  ScreenTail 轮询）；
- optimization 中未落地的 E/C 类项（Lark、多 owner、Slack/Discord 插件、配置热重载、
  断路器、state 可选后端等），实施前先对照本文"落地情况"表。

## 出处与归属

行为规格、golden 测试与 hook 脚本源自 [zhangxiangliang/herdrchat](https://github.com/zhangxiangliang/herdrchat)
（MIT）；命令目录/结构化卡/屏幕语料的知识与素材源自 [cobanov/herdrchat](https://github.com/cobanov/herdrchat)
（Apache-2.0，NOTICE 义务已履行）。明细见 [THIRD-PARTY-NOTICES](../THIRD-PARTY-NOTICES)。

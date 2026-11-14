# herdrchat Go 重写资料包

基于对 herdrchat v1.10.2（Node.js 原版）全量源码分析，为 Go 语言重写准备的文档集。**重写已开工**：进度见 [PROGRESS.md](PROGRESS.md)（M1 骨架已完成，测试全绿）。

| 文档 | 用途 |
|---|---|
| [FINAL_PLAN.md](FINAL_PLAN.md) | **最终实现方案（执行总纲）**：基线决策、cobanov 增强项并入 Go 结构（命令目录/结构化卡片/屏幕语料/能力门控/错误分类学）、上游 v1.10.2→1.12 漂移策略、修订后里程碑（M1–M8 + M7.5）与启动顺序 |
| [spec.md](spec.md) | **实现规格书**（提供给执行重写的 agent / 工程师）：目标与兼容性承诺、Go 项目结构、契约接口、hc 集成、引擎行为规范（房间/发帖/游标/对话框状态机/菜单解析算法）、三个 reader 的日志格式细节、飞书与 Telegram 实现规格、CLI/服务/hook 安装器、测试与验收标准、里程碑、常量速查表 |
| [optimization.md](optimization.md) | **优化建议**：A 必须保持的不变量 → B 架构改进（拆巨石、接口最小化、单二进制、热重载、优雅退出）→ C 性能（Telegram 分 chat 队列、hc 子进程削减、state 写放大、事件流式解析）→ D 可靠性（断路器、状态损坏恢复、flock 互斥、假 hc 对拍）→ E 功能增强（Lark、多 owner、Codex 定位修正等） |
| [reuse.md](reuse.md) | **可复用资产清单**：按 ①原样复用（ask.sh、skill、示例配置）/ ②协议格式复用（hook 协议、state.json、config.env、服务语义）/ ③知识移植（游标算法、菜单正则、reader 解析规则、平台经验）/ ④素材复用（golden 日志、文档、CI）分级，含不复用清单与落地顺序 |

四份文档配合使用：**FINAL_PLAN 定"怎么执行、增强并入哪"，spec 定"重写成什么样"，optimization 定"哪里做得更好"，reuse 定"哪些直接拿来用"**。

## 附带资产（已从原版复制进本目录）

```
assets/            可直接复用的原版文件
  hooks/ask.sh       PermissionRequest hook（逐字节复用）
  skills/herdrchat/  Skill 包（SKILL.md + 4 份 references）
  scripts/           smoke.sh / wait-release.sh
  docs/              README×2 / guide / architecture（更新后即用）
  ci/                3 个 GitHub workflows
  config.env.example / .releaserc.json / LICENSE / CHANGELOG.md

testdata/
  node-tests/       原版全部 23 个测试文件（含 fakes.mjs）
                     样本数据 + 期望断言 = Go 重写的 golden 对拍锚点
```

各文件用途与复用方式详见 [reuse.md](reuse.md) 的"目录地图"。

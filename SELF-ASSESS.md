# 自评 / Self-assessment

**总体验收：pass。** 已有 quickstart 指定的 2000 行 uniform、30000 行默认 skew、11 个真实 MySQL 集成测试以及双模块 build/vet/race/lint 均通过；完整命令与真实输出见 [VERIFICATION](VERIFICATION.md)。

工作范围：`release/v0.2.0`，由 `codex/clarity` 合入三方评审的一致通过项与本轮裁决项（`pair.go` 双 `%w`、清理验证记录、不启用私密上报渠道确认）。未改 API、数据库结构、依赖版本、LICENSE 或作者信息，未删改弱化测试。

## Rubric：11 / 12

| 项目 | 分数 / 2 | 依据与扣分原因 |
|---|---|---|
| 1. Quickstart | 2 | README 中的建库与 2000 行 uniform 命令本轮真实通过，已保存最终逐行校验成功输出；保留 DROP 范围和 skew 最小行数约束。 |
| 2. 文档体系 | 2 | 在原 docs/interface-contracts.md 与 docs/state-machine.md 上追加概念表、架构/数据流、错误码处置与当前实现状态图；保留原设计并区分未实现能力。README.en.md 镜像各 API/策略/失败边界，中文为主。 |
| 3. CI | 2 | .github/workflows/ci.yml 使用 scripts/check.sh，与本地同源覆盖双模块 build / vet、race 单测、gofmt + shell 语法 lint；独立 MySQL service 上配置集成与小规模 e2e，Skip 使门禁失败。远程 CI 未执行，见已知项。 |
| 4. 测试 | 2 | docs/testing.md 有关键路径与未覆盖风险。新增 9 个提交响应丢失子场景，以及 2 个数据计划回归；本轮双模块 race 与真实 MySQL 集成通过。根 module 跨包语句覆盖 617/716 = 86.2%，明确不是分支覆盖、未包含 e2e module。 |
| 5. 可复现验证 | 2 | VERIFICATION 逐项保存命令、真实输出及退出码，原样小规模参数失败与修正分布后的成功分别公开；列出 mock 边界。真实并发双写、三轮核对与独立 verify 在 2000 行 uniform 和 30000 行 skew 均通过。小规模必须显式 `DIST=uniform`（skew 数据集下限 27009 行）已在 quickstart 写明。 |
| 6. 运维面 | 2 | CHANGELOG 含当前无迁移说明与兼容/deprecation 策略；docs/operations.md 含权限、监控、限载、故障注入、停止恢复和切流条件；SECURITY.md 与 CONTRIBUTING.md 补齐。明确未做实际灾备演练。 |

评分依据是已交付内容，不是生产适用性认证。lint 的范围仅为格式与 shell 语法；CI 矩阵写入工作流不代表已经在这些版本上通过。

## 改动对应 / Traceability

- README.md、README.en.md、e2e/README.md：rubric 1 / 2，修正小规模命令与默认 skew 不兼容的文档陷阱。
- docs/interface-contracts.md、docs/state-machine.md：rubric 2，扩展而不推翻既有契约和设计。
- scripts/check.sh、.github/workflows/ci.yml：rubric 3 / 5，统一命令，拒绝集成 Skip 假阳性。
- replay_test.go：rubric 4，验证提交结果未知时的数据恢复与幂等，保护三种现有插入模式。
- e2e/internal/store/plan_test.go：rubric 1 / 4，验证 2000 行 uniform 可用且默认 skew 分页边界没有缩水。
- docs/testing.md、VERIFICATION.md：rubric 4 / 5，覆盖口径、真实执行证据和失败边界。
- docs/operations.md、SECURITY.md、CONTRIBUTING.md、CHANGELOG.md：rubric 6。
- SELF-ASSESS.md：六项评分、未解决项与自我质疑。

## 已知未解决项 / Unresolved

1. 原样 `ROWS=2000` 未设置 `DIST=uniform` 时仍失败：默认 skew 要求至少 27009 行。README 已给出有效命令，本轮 uniform 补跑成功；保留默认 skew 和测试断言，不改变已有行为。
2. CI 尚未远程运行；Go 1.22.x / MySQL 8.4 兼容配置待验证。本轮本地真实集成和 e2e 不替代该矩阵。
3. 已运行 30000 行 skew，未跑默认 50000 行/20000 用户、5% 故障矩阵、实际 COMMIT 响应丢失、网络/主从故障转移、进程/磁盘故障或生产容量演练。mock 与既有 Chaos 不提供这些证据。
4. 通知无可靠交付、无持久化检查点、无跨实例原子性和全局快照保证均为现有能力边界，本轮未以新功能改变语义。

## 对这版的 3 条质疑 / Three challenges

1. **86.2% 是否容易误导评审？** 根 module 的核心覆盖较高，但该分母不含独立 e2e module。本轮真实 MySQL 集成/e2e 通过，未重新生成覆盖率；仍不能把内存模型或一次实机成功误认为网络故障持久性的证明。
2. **CI 的 lint 是否足够？** 当前仅有 gofmt、shell 语法与单列的 go vet，没有 staticcheck、完整 shellcheck 或安全扫描；满足可复制的最小检查，不足以证明没有静态分析可发现的问题。未为得分临时下载依赖或夸大 lint 能力。
3. **小规模命令会不会掩盖真实负载风险？** uniform 让初见者快速运行，本轮 30000 行 skew 另验证了冻结分页边界与热点。新增测试只保护分布生成和边界不变，两次实机运行也不代表生产容量、默认 50000 行或完整锁等待行为。

本轮真实数据库证据已补齐；原样无效参数的失败仍公开保留，没有改写成 pass。

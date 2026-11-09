# Changelog / 变更记录

## Unreleased — 发布准备 / Release preparation

### Added

- 本机 MySQL 两步 quickstart、英文 README 镜像、概念与错误分类、架构/数据流及当前实现状态图。
- `scripts/check.sh` 与 CI：双模块 build / vet、无数据库 race 测试、格式和 shell 语法 lint；真实 MySQL 集成测试 Skip 使门禁失败；小规模并发双写 e2e。
- 三种插入模式的提交响应丢失回归：事务重试与新 Run 恢复，验证行值、Extra 保留与无重复修复。
- e2e 数据计划回归：小规模 uniform 的行数/路由与默认 skew 的冻结分页边界保持原有含义。
- 运维、安全、贡献与验证文档，明确通知非可靠投递、扫描高水位非时间快照、无持久化检查点等边界。

### Fixed

- `NewPair` 现在拒绝会在运行期炸的配置：空路由表清单、重复表名、`RouterFunc(tables, nil)`（原来会在第一个 key 上 panic）、`KeyColumn` 不在 `Columns` 中。属于校验加严，签名与返回类型不变。
- `mysqltable` 冲突错误改用 `%w: %w` 包装，驱动错误链不再被 `%v` 截断；报错文本与 `errors.Is(err, ErrConflict)` 行为不变。
- 最小权限说明纠正为 `SELECT + LOCK TABLES`：复核事务对两侧都做 `FOR UPDATE`，MySQL 8.0.22 起只授 `SELECT` 会在首个事务报 1142 并终止（含 Warn 模式）。
- 提交响应丢失回归补上 Insert 事务路径（原用例的批次切分使故障只落在 Update 事务上），并加断言 `faultedInserts > 0` 防止再次退化。
- e2e 接入通知阶段故障注入并输出分阶段计数（scan / lookup / txBegin / txPreCommit / notify）。
- `scripts/check.sh` 逐脚本执行 `bash -n`（原来只检查第一个文件），集成门禁额外要求至少 11 个 PASS。
- 文档撤回 `func(error) bool { return true }` 的重试建议，改为 `mysqltable.IsTransient`，避免永久错误（错片、foreign cursor）被反复重试。
- 契约补充快照模式丢应答的重试路径（盲插 → 1062 → 锁定复核），并声明自定义 `Tx.Insert` 不得实现为 upsert。
- 运维文档补充 DSN `readTimeout` / `writeTimeout` 与 `innodb_lock_wait_timeout` 要求（库本身无单步超时）。
- `mysqlErrno` 校验错误码的数字右边界：`Error 10620` 不再被误判为 `1062`（重复键）、`Error 12130` 不再被误判为 `1213`（死锁）。
- CI：同一 PR 的重复推送取消旧运行（`concurrency`），e2e 改用 `PAGE=50 BATCH=100` 覆盖跨页游标与多批事务；`push` 触发限定在 `main`，避免 PR 双跑。
- 补测：退避等待期间的上下文取消、快照插入的冲突重做上限（≤4 轮后报 `ErrConflict`）与冲突后恢复；覆盖率 86.2% → 86.5%。
- `docs/interface-contracts.md` 增加错误分类的英文版（值班人员按错误码处置不需要中文）。
- CI 首次在 GitHub Actions 实跑通过（run 42596345410，Go 1.22.x / 1.25.4 双矩阵 + MySQL 8.4 service + 跨页 e2e）。

### Migration note / 迁移说明

本次没有修改公开 API 签名、数据库结构、依赖版本或对账数据面语义；唯一的行为侧改动是冲突错误的包装方式（`%v` → `%w`），它只让 `errors.As` 能取到驱动错误，报错文本与分类结果不变。旧调用方式保持可用，无需迁移，不需要 deprecated 包装。新增检查入口不替换或削弱原测试；直接运行集成测试仍保留原有 Skip 语义，只有新发布门禁拒绝把 Skip 算作数据库验证成功。

`ROWS=2000` 的快速验证需额外传 `DIST=uniform`，因为现有默认 skew 数据集要求至少 27009 行。脚本默认值及 skew 边界分布未修改。验证记录见 VERIFICATION：build / vet / race、11 个真实 MySQL 集成测试、2000 行 uniform 与 30000 行 skew 的 e2e 均通过；`ROWS=2000` 需显式 `DIST=uniform`，默认 skew 数据集要求至少 27009 行。

## v0.1.0 — 本次工作的已存在基线 / Existing baseline

本轮从此 tag 开始；此前实现已包含 Source / Repairer / Tx 契约、三种插入模式、MySQL 分片路由与锁序校验、内存测试适配器和独立 e2e module。本条只说明基线，不追溯编造发布日期或历史发布内容。

## 版本与兼容策略 / Version and compatibility policy

- 使用语义化版本。当前 0.x 阶段仍将公开签名、配置校验、错误分类和数据面行为视为兼容面；版本号不免除兼容责任。
- 补丁版本用于兼容修复与文档；新能力可用次版本加入。API / 语义变化须同时提供 deprecated 包装或 adapter、迁移说明及变更前后数据结果等价测试，旧调用继续工作。
- 不在补丁版本移除旧入口。计划移除时须另行讨论、明确版本与支持窗口；目前没有已排期移除的 API，也未承诺多个长期支持分支。
- Go 最低版本以 go.mod 为准（当前 1.22）；提高下限或变更数据库/驱动支持范围需单独记录并验证，不借文档更新暗改。

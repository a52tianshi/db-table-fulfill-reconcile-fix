# Changelog / 变更记录

## 版本索引 / Releases

| tag | 内容 |
|---|---|
| `v0.2.0` | 合入三方评审裁决项：错误链、最小权限、丢应答测试、e2e 通知注入、门禁 |
| `v0.2.1` | 错误码数字右边界；CI `concurrency` 与跨页 e2e |
| `v0.2.2` | 补测冲突重做与退避取消；覆盖率 86.5% |
| `v0.2.3` | 错误分类英文版 |
| `v0.2.4` | 回填 GitHub Actions 实跑结果 |
| `v0.3.0` | `NewPair` 校验收紧（nil route / 重复表名 / 空路由） |
| `v0.4.0` | 20ms 延迟自测合并：Values 逐行长度校验（`ErrInvalidValues`）、路由越界拒绝、Insert 按 KeyLess 排序防死锁、错误码首头解析、ScanPage/游标校验、dblatency 断开传播、集成测试锁 fail-closed |

## v0.4.0 — 20ms 延迟自测合并 / Latency self-test merge

### 行为变更（升级前必读）/ Behavior changes — read before upgrading

公开 API 签名不变；新增导出变量 `mysqltable.ErrInvalidValues`。以下是**可观察行为**的变化，原来“能跑”的错误配置现在会报错终止：

| # | 场景 | v0.3.x 行为 | v0.4.0 行为 | 调用方如何识别 |
|---|---|---|---|---|
| 1 | `Schema.Values(row)` 返回长度 ≠ `len(Schema.Columns)` | Update：过短在持锁事务里 panic，过长被静默截断；Insert 多行：只检查总参数数，一短一长“补偿”时错位写入（列值串位后照常提交） | `Tx.Insert` 逐行、`Tx.Update` 本行在发 SQL 前校验，返回 `ErrInvalidValues`，事务回滚、锁释放 | `errors.Is(err, mysqltable.ErrInvalidValues)`；`IsTransient` 为 false，不重试 |
| 2 | `Router.Route(k)` 返回不在 `Tables()` 里的表 | Lookup 查未登记表（可能得到假 clean），Insert/Update 写进未登记表（幽灵写入） | Lookup / Lock / Insert / Update 在发 SQL 前返回 `*MisroutedError`（`Expected=""`），Run 终止 | `errors.Is(err, mysqltable.ErrMisrouted)` / `errors.As(err, &*MisroutedError)`；**不新增** `ErrInvalidRoute`，路由越界与错片共用 `ErrMisrouted` 一个 sentinel |
| 3 | `Tx.Insert` 的写入顺序 | 按调用方给的行序切 chunk（分片老表扫出来可能是 `[2,1]`），与按升序加锁的线上写入方交叉 → 真 MySQL 1213 死锁 | 每张目标表内先按 `Schema.KeyLess` 稳定升序排序再切 chunk | 无需改代码；自定义 `Tx` 实现也应遵守同一顺序 |
| 4 | `IsTransient` / `IsDuplicateKey` 解析错误串 | 子串匹配，业务数据里出现 `Error 1213`、`i/o timeout` 等字样可被误判 | 只解析错误串里**第一个** `Error <digits>` 头；已识别出服务端错误码后不再落入 `i/o timeout` / `unexpected EOF` 等子串匹配 | 无需改代码；依赖旧误判重试的调用方会看到更早失败 |
| 5 | `ScanPage(ctx, cursor, limit)` 的 `limit <= 0` | panic | 返回错误（`limit must be > 0` 类参数错误，非 transient） | 检查 error |
| 6 | 游标 | 未绑定表名，跨侧/跨表复用游标可能静默从错误位置继续 | 游标绑定表名；跨侧/跨表游标返回 foreign cursor 错误（永久错误，`IsTransient` 为 false） | 不要在 Old / New 或不同 Pair 之间复用游标 |

### Fixed

- `mysqltable`：上表 1–6。`ErrInvalidValues` 为新增导出 sentinel（原为未导出的 `errInvalidValues`），godoc 说明它是配置错误、不可重试；`ErrMisrouted` 的 godoc 写明它同时覆盖“Route 越界”。
- `e2e/cmd/dblatency`：任一方向断开即关闭两端。原来客户端 ctx 取消关闭 socket 后，代理不向 MySQL 传播关闭，服务端事务以 `Sleep` 持锁存活到 `wait_timeout`，导致后续测试 1205。
- `e2e/mysqltable_it` 的 `TestMain`：MySQL 连不上仍让测试自行 Skip（发布门禁把 Skip 当失败）；**连上了但 `GET_LOCK` 拿不到（出错或 900s 超时）直接失败退出**，不再“best effort 不加锁继续跑”。

### Tests

- 新增（移植自 codex 自测分支）：`Insert` 多行“各行 Values 一短一长、参数总数刚好凑齐”的错位写入用例——单测 `mysqltable.TestTxInsertRejectsCompensatingValuesLengths`、真 MySQL `TestInsertRejectsCompensatingValuesLengths`；已做变异检查（去掉 Insert 逐行校验 → 两者均红）。
- 既有 Values 长度用例的断言收紧为 `errors.Is(err, ErrInvalidValues) && !IsTransient(err)`（原为 `err != nil`）。
- 20ms RTT 延迟代理下的 unit / integration / e2e 结果见 VERIFICATION 第 11 节。

### 已知未做 / Not done

- Different 修复仍是每行一个 UPDATE 往返，持锁时间 ∝ `TxBatchSize × RTT`（见 docs/operations.md）；批量 UPDATE 需要 `Tx` 接口变更，本版不做。
- 百万行规模未测；COMMIT 结果未知的确定性窗口仍需调用方重跑确认（见 operations“停止与恢复”）。


## v0.2.0 – v0.3.0

以下小节是 `v0.2.0`–`v0.3.0` 的合并记录（同一天发布，未按 tag 拆节）。

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
- 补测：退避等待期间的上下文取消、快照插入的冲突重做上限（首轮之外最多 3 轮，即最多 4 次锁定复核后报 `ErrConflict`）与冲突后恢复；覆盖率 86.2% → 87.1%。
- `docs/interface-contracts.md` 增加错误分类的英文版（值班人员按错误码处置不需要中文）。
- CI 首次在 GitHub Actions 实跑通过（run 42596345410，Go 1.22.x / 1.25.4 双矩阵 + MySQL 8.4 service + 跨页 e2e）。

### Migration note / 迁移说明

本次没有修改公开 API 签名、数据库结构或依赖版本，但有三项行为侧变化，升级前请对照：

1. **冲突错误包装**：`mysqltable` 用 `%w: %w` 包装主键冲突（原先 `%v` 截断驱动错误链）。报错文本与 `errors.Is(err, ErrConflict)` 结果不变，新增的是 `errors.As` 能取到驱动错误。
2. **配置校验加严**：`NewPair` 现在拒绝空路由、重复表名、`RouterFunc(tables, nil)`、`KeyColumn` 不在 `Columns` 中、只有主键列没有值列的 `Schema`。此前这些配置能在 `NewPair` 通过，但会在运行期 panic 或生成非法 SQL（`UPDATE t SET  WHERE ...`）。
3. **错误码识别边界**：`IsDuplicateKey` / `IsTransient` 不再把 `Error 10620` 当成 1062、`Error 12130` 当成 1213。旧调用方式保持可用，无需迁移，不需要 deprecated 包装。新增检查入口不替换或削弱原测试；直接运行集成测试仍保留原有 Skip 语义，只有新发布门禁拒绝把 Skip 算作数据库验证成功。

`ROWS=2000` 的快速验证需额外传 `DIST=uniform`，因为现有默认 skew 数据集要求至少 27009 行。脚本默认值及 skew 边界分布未修改。验证记录见 VERIFICATION：build / vet / race、11 个真实 MySQL 集成测试、2000 行 uniform 与 30000 行 skew 的 e2e 均通过；`ROWS=2000` 需显式 `DIST=uniform`，默认 skew 数据集要求至少 27009 行。

## v0.1.0 — 本次工作的已存在基线 / Existing baseline

本轮从此 tag 开始；此前实现已包含 Source / Repairer / Tx 契约、三种插入模式、MySQL 分片路由与锁序校验、内存测试适配器和独立 e2e module。本条只说明基线，不追溯编造发布日期或历史发布内容。

## 版本与兼容策略 / Version and compatibility policy

- 使用语义化版本。当前 0.x 阶段仍将公开签名、配置校验、错误分类和数据面行为视为兼容面；版本号不免除兼容责任。
- 补丁版本用于兼容修复与文档；新能力可用次版本加入。API / 语义变化须同时提供 deprecated 包装或 adapter、迁移说明及变更前后数据结果等价测试，旧调用继续工作。
- 不在补丁版本移除旧入口。计划移除时须另行讨论、明确版本与支持窗口；目前没有已排期移除的 API，也未承诺多个长期支持分支。
- Go 最低版本以 go.mod 为准（当前 1.22）；提高下限或变更数据库/驱动支持范围需单独记录并验证，不借文档更新暗改。

# 运维手册 / Operations

本手册建立在 [接口契约](interface-contracts.md) 与 [状态机](state-machine.md) 上。实际执行证据见 [VERIFICATION](../VERIFICATION.md)，未覆盖风险见 [测试口径](testing.md)。

## 接入与上线 / Rollout

1. 明确老表是权威源，确认新旧表列结构相同、主键唯一、`K` 中主键和分片字段不可变。路由、表清单和字符串 collation 必须与线上写入一致。
2. 为本库使用单独账号：**老表需要 `SELECT` + `LOCK TABLES`**——`mysqltable` 的复核事务对两侧都执行 `SELECT ... FOR UPDATE`，而 MySQL 8.0.22 起锁定读还要求会话对表具备 `UPDATE`、`DELETE`、`LOCK TABLES` 三者之一，只授 `SELECT` 会在第一个复核事务上报 `1142`，且该错误不属于 `IsTransient`，`Run` 会直接退出（`Missing=Warn` 的 Warn 模式同样如此，因为它也要复核）。新表需要 `SELECT` / `INSERT` / `UPDATE`。库不会 `DELETE` 或建表；e2e 的建库和 `DROP` 权限仅属于隔离测试账号。生产账号不使用 root。

3. 先验证业务同事务双写：老表先锁、新表后锁；同一侧内先按**物理表名字典序**、再按主键序锁定（不是全局主键序，分表时一个事务会跨多张老表）。适配器只支持一个 `*sql.DB` 内的事务；跨实例必须另行实现 Repairer 并证明一致性边界。
4. 先以 `Missing=Warn, Different=Warn` 运行，检查 Extra、错片、差异规模。Warn 仍使用复核事务和行锁，不是零负载探测。
5. 确认权威源及数据保留要求后启用 Fix，通常选 `InsertBatchLocked`。快照模式仅在老表不删除、Different=Fix 且收敛前新表不对外读时使用。
6. 每轮同时保存 error、Report、Progress 的最终回调、配置和边界。启用 CheckExtra，确认双写健康，再做独立校验；e2e 的 verify 使用同一一致性快照逐行核对并检查落片。

不要用固定“三轮”作为任意业务的切流依据。e2e 三轮只是该模拟负载的断言，不证明实际流量、删除模型、字符串主键或跨实例场景。

## 资源与监控 / Capacity and signals

- 引擎串行、有界攒批；配置 `ScanPageSize`、`BatchSize`、`TxBatchSize` 和适配器 `InChunk` / `InsertChunk` 共同控制 SQL 数量、锁时长和峰值内存。`MemoryLimit` 只限制估算缓存批，不是 RSS；驱动、扫描页和查询结果另占内存。
- 库没有每秒行数限流或跨任务互斥。由调用方限制调度频率、连接池与同时运行的任务数量；先按代表性数据观察 DB CPU、锁等待、连接数和延迟，再调整批大小。
- **连接与锁超时必须配在 DSN 上**：库没有单步/单事务超时；`ctx` 会传给 `BeginTx` / `QueryContext` / `ExecContext`，是否中断取决于驱动——go-sql-driver 会在取消时中断语句并关闭连接，因此**在 COMMIT 途中取消属于提交结果未知**，处置同下文“停止与恢复”第 4 步。生产 DSN 至少设置 `readTimeout`、`writeTimeout`、`timeout`（连接），并把 `innodb_lock_wait_timeout` 调到业务可接受的上限，否则慢 SQL 或网络半开会长期持有行锁、阻塞线上双写。
- 监控 Run error、完成耗时、`Retries` 增长、Extra、连续多轮 Missing/Different、`Inserted/Updated` 和双写错误；指标阈值由业务容量演练确定，本库没有通用 SLA。
- `Suspected` 与 `Stale` 在并发双写时可以增加；`Warned` 不等于告警投递成功数。Progress 同步执行，回调阻塞会延迟扫描；使用有界异步投递并明确队列满策略。
- `LogNotifier` 会包含 key / 差异信息。不要无筛选记录完整业务行、DSN 或凭据。可靠通知需业务持久化 outbox / 恢复协议；重复 Run 不会保证重放已经修复的 Finding。

## 故障注入 / Fault injection

现有 `e2e/internal/store/chaos.go` 在 ScanPage、Lookup、事务开始前和回调完成后但 COMMIT 前注入伪造 1213 错误。事务末尾注入会使已执行的 SQL 回滚；**不会模拟“实际已提交但响应丢失”**。随机种子来自时间，注入次数与具体落点不是固定输出。数据和成功判据才是应复现的结果。

以下在仓库根目录运行，仅使用自己的测试库；每次完整脚本都会重建测试表：

```sh
# 默认 2% 注入 + 持续双写 + 最终独立逐行校验
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" DIST=uniform ROWS=2000 USERS=1000 WARMUP=2 GAP=3 ./e2e/run.sh

# 锁定插入、互不整除的批次、内存阈值、5% 注入
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" DIST=uniform ROWS=2000 USERS=1000 WARMUP=2 GAP=3 INSERT_MODE=batch-locked PAGE=97 BATCH=250 TX_BATCH=17 MEMORY_LIMIT=8000 FAIL_RATE=0.05 ./e2e/run.sh

# 上一条脚本构建并建表后，确定性地在扫描前失败；预期非零退出
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" ./e2e/bin/e2e reconcile -fail-rate 1 -retry-attempts 3
```

最后一条应在第 3 次失败后返回 `giving up after 3 attempts`，不把失败页判作缺失。第一条 2% 注入的 uniform 命令本轮已通过；后两条 5% 注入和确定性耗尽命令本轮未执行。确切尝试范围见 VERIFICATION，不能把此处操作说明当执行记录。

`replay_test.go` 在真实 memtable 提交后返回一次模拟连接错误，验证三种插入模式自动重试或新 Run 恢复后行内容正确且不重复插入。这只证明引擎对 Repairer 契约的处理，不能替代 MySQL 的网络代理断连、进程 kill、主从故障转移或磁盘故障演练。

## 停止、恢复与故障处置 / Stop and recover

1. 停止由调用方取消 Run 的 context；等待返回并记录最终 Progress。已提交事务保留；尚未成功提交的事务由 Repairer 回滚。不要假定 error 意味着整轮回滚。
2. 1213 / 1205 先检查线上锁序和事务大小；1040 检查连接池与并发任务；权限、语法、缺表属于配置或部署问题，不应靠无限重试掩盖。
3. 错片停止自动修复，由业务确认 Route 与物理落片；先备份和审批再人工搬迁。库不会为得到“无差异”而删 Extra。
4. COMMIT 结果未知时保持双写，恢复连接后用同一配置重跑。锁定重读或快照冲突后的复核会重新确认状态；Report 的 Inserted/Updated 可能偏小，必须检查实际数据。
5. 通知失败时数据可能已全部提交。根据通知端的持久化记录恢复事件；重新 Run 只能恢复数据核对，不保证找回通知。
6. 没有持久化检查点：崩溃后从头 Run。没有一键回滚整轮修复；若权威源或比较规则配置错误，先停任务、保护老表、从备份或审计数据按业务方案恢复。

## 版本与支持 / Compatibility

发布、deprecation 与迁移规则见 [CHANGELOG](../CHANGELOG.md)。根 module 的 Go 下限为 1.22，且不捆绑 SQL 驱动；e2e 是独立 module，已有 MySQL 驱动仅用于验证。CI 配置 Go 1.22.x / 1.25.4 与 MySQL 8.4，本次验证环境为 Go 1.25.4 / MySQL 9.7.1，真实 MySQL 集成、2000 行 uniform 与 30000 行 skew 的 e2e 已通过。不能据此承诺所有 MySQL 版本或 MariaDB 兼容。

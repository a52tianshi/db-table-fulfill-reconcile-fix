# 自测报告（Agent: agy）

- **工作区**：`/Users/chenquan/work/bakeoff/dbtable/t2/agy`（分支 `t2/agy`，基线提交 `088cc99` `v0.3.2`）
- **测试环境**：`export E2E_MYSQL_DSN="root@tcp(127.0.0.1:3313)/reconcile_t2a"`（本机 `dblatency` 20ms RTT 延迟代理，专属库 `reconcile_t2a`，集成测试共享库 `reconcile_it`）

---

## 1. 基线：命令 + 结果 + 耗时

在不修改任何代码和脚本的前提下，先完整执行了三条基线验证：

### 1.1 Unit / Build / Vet / Lint 基线

- **命令**：
  ```bash
  ./scripts/check.sh
  ```
- **结果**：PASS（全部单元包 + `go vet` + `gofmt -l` + `bash -n` 零失败）
- **耗时**：总墙钟 **5.181s**（`2.33s user 1.88s system`）
- **关键输出**：
  ```text
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix	1.903s
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable	1.462s
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable	2.164s
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store	1.506s
  ?   	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/cmd/dblatency	[no test files]
  ?   	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/cmd/e2e	[no test files]
  lint PASS (gofmt + bash -n, per script)
  ```

### 1.2 Integration 基线（20ms RTT 延迟下）

- **命令**：
  ```bash
  E2E_MYSQL_DSN="root@tcp(127.0.0.1:3313)/reconcile_t2a" ./scripts/check.sh integration
  ```
- **结果**：PASS（11 个顶层测试 + 2 个子测试全部通过，无 Skip）
- **耗时**：包耗时 **130.140s**（墙钟 `2:10.72`，含等待 `GET_LOCK('db_table_fulfill_reconcile_it_suite', 900)` 约 63s；测试本体约 67s，其中 `TestRunConvergesWithOddSizes` 单测耗时 **56.08s**）
- **关键输出**：
  ```text
  === RUN   TestScanPageUnevenShards
  --- PASS: TestScanPageUnevenShards (1.71s)
  === RUN   TestScanStopsAtHighWaterMark
  --- PASS: TestScanStopsAtHighWaterMark (0.83s)
  === RUN   TestLookupRoutesAndConfirmsAbsence
  --- PASS: TestLookupRoutesAndConfirmsAbsence (0.76s)
  === RUN   TestTxInsertUpdateConflictRollback
  --- PASS: TestTxInsertUpdateConflictRollback (1.89s)
  === RUN   TestLockHoldsRowLocks
  --- PASS: TestLockHoldsRowLocks (1.87s)
  === RUN   TestRunConvergesWithOddSizes
  --- PASS: TestRunConvergesWithOddSizes (56.08s)
  === RUN   TestWrongShardStopsRunWithoutDeleting
  === RUN   TestWrongShardStopsRunWithoutDeleting/extra
  === RUN   TestWrongShardStopsRunWithoutDeleting/duplicate
  --- PASS: TestWrongShardStopsRunWithoutDeleting (1.37s)
      --- PASS: TestWrongShardStopsRunWithoutDeleting/extra (0.57s)
      --- PASS: TestWrongShardStopsRunWithoutDeleting/duplicate (0.80s)
  === RUN   TestCorrectlyRoutedExtraWarnsAndRemains
  --- PASS: TestCorrectlyRoutedExtraWarnsAndRemains (0.91s)
  === RUN   TestLockSortsAcrossChunksAgainstOnlineWriter
  --- PASS: TestLockSortsAcrossChunksAgainstOnlineWriter (1.03s)
  === RUN   TestHighWaterIsKeyBoundNotTemporalSnapshot
  --- PASS: TestHighWaterIsKeyBoundNotTemporalSnapshot (0.71s)
  === RUN   TestWrongShardInLockRollsBackEarlierUpdate
  --- PASS: TestWrongShardInLockRollsBackEarlierUpdate (0.88s)
  PASS
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/mysqltable_it	130.140s
  integration PASS (no test skipped)
  ```

### 1.3 E2E 基线（20ms RTT 延迟 + 在线并发写入 + 故障注入）

- **命令**：
  ```bash
  ROWS=2000 USERS=1000 DIST=uniform WARMUP=2 GAP=3 \
    E2E_MYSQL_DSN="root@tcp(127.0.0.1:3313)/reconcile_t2a" ./e2e/run.sh
  ```
- **结果**：`E2E PASS`（但结束后在 `SHOW FULL PROCESSLIST` / `information_schema.innodb_trx` 中抓到了 `dblatency` 代理导致僵尸连接与未提交事务锁泄漏的 P0/P1 缺陷，详见第 3 节）
- **耗时**：总墙钟 **164.96s（2m44.96s）**（无延迟时同规模约 2.5s，20ms RTT 放大约 65 倍）
- **关键输出**：
  ```text
  [init] seed=1794248574940682000 old=2000 users=1000 preCopied=0 preDirty=0 in 5.651s
  [writer] started workers=8 known=2000 insertRatio=0.30
  [run1] insert=batch-snapshot injected=52 (scan=3 lookup=0 txBegin=6 txPreCommit=3 notify=40) scanned old=2026 new=2481 suspected=2000 stale=0 | missing=2000 different=0 extra=0 | inserted=2000 updated=0 warned=0 | retries=52 42.207s
  [run2] insert=batch-snapshot injected=6 (scan=3 lookup=0 txBegin=0 txPreCommit=3 notify=0) scanned old=2549 new=3248 suspected=261 stale=89 | missing=0 different=172 extra=0 | inserted=0 updated=172 warned=0 | retries=6 58.139s
  [run2] PASS: no missing/extra; 172 stale snapshot rows overwritten
  [run3] insert=batch-snapshot injected=2 (scan=0 lookup=1 txBegin=1 txPreCommit=0 notify=0) scanned old=3299 new=3700 suspected=67 stale=67 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=2 37.015s
  [run3] PASS: nothing to repair (67 stale candidates resolved by reread)
  [writer] stopped inserts=1749 updates=4207 failed=0
  [verify] new shard sizes: empty=0 below1000=100 min=28 max=48
  [verify] old=3749 new=3749 checked=3749 missing=0 different=0 misrouted=0 extra=0 | 15.079s
  [verify] PASS: old and new tables are identical
  E2E PASS
  ```

---

## 2. 新增测试清单

本轮聚焦高风险、20ms 延迟下放大明显或现有覆盖空白的 **6 个核心方向**（对应任务方向 1、2、4、6、7、8、9 及延迟代理自身连接生命周期），共新增 **12 个顶层测试函数**（含 6 个单元/代理测试、6 个真实 MySQL 20ms 延迟集成测试）：

| 序号 | 文件 | 测试函数 | 覆盖方向与测什么 | 修复前为什么红（具体断言与错误） |
| :--- | :--- | :--- | :--- | :--- |
| 1 | `e2e/cmd/dblatency/main_test.go` | `TestProxyConnClosesBackendOnClientDisconnect` | **延迟代理与 ctx 取消/进程强杀**：客户端断开（如 `kill -TERM $WRITER` 或 `ctx` 超时导致驱动关闭连接）但服务端空闲时，`dblatency` 是否及时关闭后端 MySQL 连接以释放 InnoDB 行锁/MDL/`GET_LOCK` | 修复前 `defer server.Close()` 在 `wg.Wait()` 之后执行；服务端不发数据时 `copyDelayed(client, server)` 永久阻塞在 `server.Read(buf)`，导致后端连接永不关闭：<br>`main_test.go:82: dblatency leaked backend connection after client disconnect: backend never received EOF` |
| 2 | `mysqltable/errno_test.go` | `TestErrorClassifierIgnoresPayloadSubstringsAndPermanentWrappers` | **方向 4/2（重试分类与错误码欺骗）**：测试 `IsTransient` 与 `IsDuplicateKey` 面对业务主键/表名中包含 `'Error 1213: ...'`、`'Error 1062: ...'`、`'i/o timeout'`、`'unexpected EOF'` 时的分类准确性，以及被包装的永久错误（`errInvalidValues`）不被误判为可重试 | 修复前 `mysqlErrno` 对整串 `err.Error()` 搜 `"Error "+code` 且 `IsTransient` 在非瞬态 MySQL 错误码后继续落入子串匹配：<br>1. `IsTransient(Error 1062 ... 'Error 1213: fake deadlock' ...) = true, want false`<br>2. `IsTransient(Error 1062 ... 'i/o timeout' ...) = true, want false`<br>3. `IsTransient(Error 1146 ... 'db.unexpected EOF' ...) = true, want false`<br>4. `IsDuplicateKey(Error 1205 ... 'Error 1062: dup') = true, want false` |
| 3 | `mysqltable/regression_test.go` | `TestTxUpdateAndInsertRejectWrongValuesLength` | **方向 8（`Tx.Update` / `Tx.Insert` 列值长度校验）**：当 `Schema.Values(row)` 返回长度 `< len(Columns)` 或 `> len(Columns)` 时，`Update` 和批量 `Insert` 必须返回明确错误，不能 panic、不能静默截断、不能在多行 `INSERT` 中错位拼接列值 | 修复前：<br>1. `Update` 短切片直接越界 panic：`Update panicked instead of returning error: runtime error: index out of range [1] with length 1`<br>2. `Update` 长切片静默截断返回 `nil`：`expected error when Values returns more items than Columns`<br>3. `Insert` 多行（3+1=4 个参数喂给 2 行×2 列）静默把第 1 行多出的列塞进第 2 行主键：`expected error when Values length mismatches Columns in Insert` |
| 4 | `mysqltable/regression_test.go` | `TestInsertSortsByKeyLessBeforeChunking` | **方向 7（`Tx.Insert` 切分前未按 `KeyLess` 排序）**：验证单分片内多行 `Insert` 在按 `InsertChunk` 切分前必须按 `Schema.KeyLess` 排序，保证加锁顺序确定 | 修复前按调用方传入切片顺序直接切块：<br>`Insert key order across chunks = [[10 2] [9 1]], want [[1 2] [9 10]]` |
| 5 | `mysqltable/regression_test.go` | `TestRouteOutsideTablesRejectedAcrossOperations` | **方向 6（路由越界 `Route(k)` 返回不在 `Tables()` 中的表名）**：验证 `Lookup`、`Lock`、`Tx.Insert`、`Tx.Update` 均拒绝路由到 `Tables()` 之外的表并返回 `ErrMisrouted` | 修复前仅 `ScanPage` 校验了 `Route(k)`，`Lookup`/`Lock`/`Insert`/`Update` 直接拼 SQL 访问未登记表：<br>`Lookup: want ErrMisrouted, got <nil>` |
| 6 | `mysqltable/regression_test.go` | `TestCrossSideCursorAndZeroLimitRejected` | **方向 3/6（游标边界与跨表/跨侧游标误用）**：验证 `ScanPage(ctx, nil, 0)` 不发生越界 panic，且把 `New()` 的游标传给 `Old().ScanPage` 被识别为非法外部游标 | 修复前：<br>1. `ScanPage(limit=0) panicked: runtime error: index out of range [-1]`<br>2. `scanCursor` 不记录表名，`New` 游标 (`shard=1`) 传给单表 `Old` 时 `c.shard >= len(tables)` 静默返回 `nil, nil, nil`：`expected foreign cursor error when passing New cursor to Old.ScanPage` |
| 7 | `e2e/mysqltable_it/regression_test.go` | `TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks` | **方向 8（真实 MySQL + 20ms 延迟下持锁事务内 `Values` 长度异常）**：验证 `Run` 在 `SELECT ... FOR UPDATE` 持锁后遇到 `Values` 长度过短/过长时不会 panic、不污染新表、且回滚释放锁（后续写入立即成功） | 修复前：<br>1. `short_Values`: `Run panicked inside locked transaction: runtime error: index out of range [3] with length 3`<br>2. `long_Values`: `expected error from long Values, got nil (report=... updated=1 ...)` |
| 8 | `e2e/mysqltable_it/regression_test.go` | `TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter` | **方向 7（老表分表 + `InsertBatchSnapshot` + 在线写入死锁回归）**：老表 2 个分片（分片 0 含 key=2，分片 1 含 key=1），`ScanOld` 按分片顺序产出 `[2, 1]`；`InsertBatchSnapshot` 下 `SELECT ... FOR UPDATE` 对新表缺失行不加锁，随后 `Tx.Insert` 与按升序锁 `(1, 2)` 的在线事务并发执行 | 修复前 `Tx.Insert` 先插 key=2（持锁 2），再插 key=1（等待锁 1），与持锁 1 等待锁 2 的在线事务在 20ms 延迟窗口内 100% 触发真实 MySQL 1213 死锁：<br>`online writer deadlocked or failed on key 2 because Tx.Insert locked key 2 before key 1: Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction` |
| 9 | `e2e/mysqltable_it/regression_test.go` | `TestRouteReturningUnlistedTableStopsWithoutWriting` | **方向 6（真实 MySQL 下 `New.Route(k)` 越界导致幽灵写入与假阳性收敛）**：`Tables()` 仅含 `it_new_0..2`，但 `Route(3)` 返回真实存在却未纳入 `Tables()` 的 `it_new_3` | 修复前 `Run` 把 `id=3` 写入 `it_new_3` 且返回 `err == nil`，重跑 `Run` 扫描不到 `it_new_3` 却报告 `Consistent() == true`：<br>`expected ErrMisrouted when Route(k) returns table outside Tables(), got rep=scanned old=1 new=0 suspected=1 stale=0 | missing=1 different=0 extra=0 | inserted=1 updated=0 warned=0 | retries=0 448ms err=<nil>` |
| 10 | `e2e/mysqltable_it/regression_test.go` | `TestRealMySQLLostCommitAckOnSnapshotInsertRecoversWithConcurrentUpdate` | **方向 1（真实 MySQL `COMMIT` 途中 `ctx` 取消「提交结果未知」+ 并发更新下恢复幂等）**：包装真实 MySQL 驱动 `driver.Tx.Commit()`，在真实 `COMMIT` 落盘后模拟返回 `context.DeadlineExceeded (driver.ErrBadConn)`，同时在线 writer 已将刚插入的新表行更新为 `v=999`；验证同一 `Run` 内重试能识别 1062 Duplicate Key、重读并保留在线更新 `v=999` | 验证真实 MySQL + 20ms 延迟下方向 1 恢复路径（`rep.Inserted == 4`、`rep.Retries >= 1`，在线并发更新 `id=2 -> v=999` 未被旧快照覆盖） |
| 11 | `e2e/mysqltable_it/regression_test.go` | `TestRealMySQLLockWaitTimeoutRetriedAndConverges` | **方向 2（真实 MySQL `innodb_lock_wait_timeout` Error 1205 重试收敛）**：将会话 `innodb_lock_wait_timeout` 设为 1s，外部事务持锁 `id=2` 达 1.4s 触发真实 MySQL `Error 1205 (HY000): Lock wait timeout exceeded`；验证 `IsTransient` 识别 1205 并退避重试直至锁释放后修复完成 | 验证真实 MySQL 1205 锁等待超时在 20ms 延迟下被正确归类为 `Retryable` 并在单次 `Run` 内收敛（`rep.Retries >= 1`, `rep.Updated == 1`） |
| 12 | `e2e/mysqltable_it/regression_test.go` | `TestMidRunTimeoutKillAndRerunIdempotent` | **方向 9（一次 `Run` 中途超时中断后重跑幂等性）**：写入 30 行 missing + 10 行 different + 5 行 extra，第一次 `Run` 设置 `350ms` 超时在修复中途中断（仅完成部分批次），随后用充足时间重跑第 2、第 3 次 `Run` | 验证中断重跑不会产生重复行、不会丢失 `Extra` 告警（第 2 次 `Warned == 5`），且不删除新表 Extra 行，第 3 次重跑 `Inserted == 0 && Updated == 0` |

### 2.1 「回退修复 → 新测试变红」与「恢复修复 → 新测试变绿」复跑证据

#### （A）单元测试与代理测试回退变红证据

- **回退后执行命令**：
  ```bash
  go test -v ./mysqltable -run "TestErrorClassifierIgnores|TestTxUpdateAndInsert|TestInsertSorts|TestRouteOutside|TestCrossSide"
  (cd e2e && go test -v ./cmd/dblatency)
  ```
- **回退修复时（RED）输出片段**：
  ```text
  === RUN   TestErrorClassifierIgnoresPayloadSubstringsAndPermanentWrappers
      errno_test.go:79: IsTransient("Error 1062 (23000): Duplicate entry 'Error 1213: fake deadlock' for key 'PRIMARY'") = true, want false
      errno_test.go:79: IsTransient("Error 1062 (23000): Duplicate entry 'i/o timeout' for key 'PRIMARY'") = true, want false
      errno_test.go:79: IsTransient("Error 1146 (42S02): Table 'db.unexpected EOF' doesn't exist") = true, want false
      errno_test.go:84: IsDuplicateKey("Error 1205 (HY000): Lock wait timeout; info 'Error 1062: dup'") = true, want false
  --- FAIL: TestErrorClassifierIgnoresPayloadSubstringsAndPermanentWrappers (0.00s)
  === RUN   TestTxUpdateAndInsertRejectWrongValuesLength
      regression_test.go:53: Update panicked instead of returning error: runtime error: index out of range [1] with length 1
      regression_test.go:62: expected error when Values returns more items than Columns
      regression_test.go:95: expected error when Values length mismatches Columns in Insert
  --- FAIL: TestTxUpdateAndInsertRejectWrongValuesLength (0.00s)
  === RUN   TestInsertSortsByKeyLessBeforeChunking
      regression_test.go:154: Insert key order across chunks = [[10 2] [9 1]], want [[1 2] [9 10]]
  --- FAIL: TestInsertSortsByKeyLessBeforeChunking (0.00s)
  === RUN   TestRouteOutsideTablesRejectedAcrossOperations
      regression_test.go:187: Lookup: want ErrMisrouted, got <nil>
  --- FAIL: TestRouteOutsideTablesRejectedAcrossOperations (0.00s)
  === RUN   TestCrossSideCursorAndZeroLimitRejected
      regression_test.go:240: ScanPage(limit=0) panicked: runtime error: index out of range [-1]
      regression_test.go:258: expected foreign cursor error when passing New cursor to Old.ScanPage
  --- FAIL: TestCrossSideCursorAndZeroLimitRejected (0.00s)
  FAIL
  FAIL	github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable	0.415s

  === RUN   TestProxyConnClosesBackendOnClientDisconnect
      main_test.go:82: dblatency leaked backend connection after client disconnect: backend never received EOF
  --- FAIL: TestProxyConnClosesBackendOnClientDisconnect (0.31s)
  FAIL
  FAIL	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/cmd/dblatency	0.935s
  ```

- **恢复修复后复跑命令（GREEN）**：
  ```bash
  go test -v ./mysqltable -run "TestErrorClassifierIgnores|TestTxUpdateAndInsert|TestInsertSorts|TestRouteOutside|TestCrossSide"
  (cd e2e && go test -v ./cmd/dblatency)
  ```
- **恢复修复后（GREEN）输出**：
  ```text
  --- PASS: TestErrorClassifierIgnoresPayloadSubstringsAndPermanentWrappers (0.00s)
  --- PASS: TestTxUpdateAndInsertRejectWrongValuesLength (0.00s)
  --- PASS: TestInsertSortsByKeyLessBeforeChunking (0.00s)
  --- PASS: TestRouteOutsideTablesRejectedAcrossOperations (0.00s)
  --- PASS: TestCrossSideCursorAndZeroLimitRejected (0.00s)
  PASS
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable	0.369s

  --- PASS: TestProxyConnClosesBackendOnClientDisconnect (0.02s)
  PASS
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/cmd/dblatency	0.440s
  ```

#### （B）真实 MySQL 20ms 延迟集成测试回退变红证据

- **回退修复时（RED）输出片段**：
  ```text
  === RUN   TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks
  === RUN   TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks/short_Values_does_not_panic_under_lock_and_rolls_back
  === RUN   TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks/long_Values_rejected_instead_of_silent_truncation
  --- FAIL: TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks (1.89s)
      --- FAIL: TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks/short_Values_does_not_panic_under_lock_and_rolls_back (0.98s)
          regression_test.go:97: Run panicked inside locked transaction: runtime error: index out of range [3] with length 3
      --- FAIL: TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks/long_Values_rejected_instead_of_silent_truncation (0.91s)
          regression_test.go:138: expected error from long Values, got nil (report=scanned old=1 new=1 suspected=1 stale=0 | missing=0 different=1 extra=0 | inserted=0 updated=1 warned=0 | retries=0 557ms)
  === RUN   TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter
      regression_test.go:255: online writer deadlocked or failed on key 2 because Tx.Insert locked key 2 before key 1: Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction
  --- FAIL: TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter (2.63s)
  === RUN   TestRouteReturningUnlistedTableStopsWithoutWriting
      regression_test.go:303: expected ErrMisrouted when Route(k) returns table outside Tables(), got rep=scanned old=1 new=0 suspected=1 stale=0 | missing=1 different=0 extra=0 | inserted=1 updated=0 warned=0 | retries=0 448ms err=<nil>
  --- FAIL: TestRouteReturningUnlistedTableStopsWithoutWriting (0.88s)
  ```

- **恢复修复后复跑命令（GREEN）**：
  ```bash
  (cd e2e && E2E_MYSQL_DSN="root@tcp(127.0.0.1:3313)/reconcile_t2a" go test -v ./mysqltable_it -run "TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks|TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter|TestRouteReturningUnlistedTableStopsWithoutWriting|TestRealMySQLLostCommitAckOnSnapshotInsertRecoversWithConcurrentUpdate|TestRealMySQLLockWaitTimeoutRetriedAndConverges|TestMidRunTimeoutKillAndRerunIdempotent")
  ```
- **恢复修复后（GREEN）输出**：
  ```text
  === RUN   TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks
  --- PASS: TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks (1.33s)
      --- PASS: TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks/short_Values_does_not_panic_under_lock_and_rolls_back (0.42s)
      --- PASS: TestUpdateAndInsertRejectMismatchedValuesAndReleaseLocks/long_Values_rejected_instead_of_silent_truncation (0.39s)
  === RUN   TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter
  --- PASS: TestSnapshotInsertFromShardedOldSortsKeysAgainstOnlineWriter (3.41s)
  === RUN   TestRouteReturningUnlistedTableStopsWithoutWriting
  --- PASS: TestRouteReturningUnlistedTableStopsWithoutWriting (0.57s)
  === RUN   TestRealMySQLLostCommitAckOnSnapshotInsertRecoversWithConcurrentUpdate
  --- PASS: TestRealMySQLLostCommitAckOnSnapshotInsertRecoversWithConcurrentUpdate (1.28s)
  === RUN   TestRealMySQLLockWaitTimeoutRetriedAndConverges
  --- PASS: TestRealMySQLLockWaitTimeoutRetriedAndConverges (2.44s)
  === RUN   TestMidRunTimeoutKillAndRerunIdempotent
  --- PASS: TestMidRunTimeoutKillAndRerunIdempotent (3.69s)
  PASS
  ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/mysqltable_it	13.275s
  ```

---

## 3. 发现的问题清单（分级、修复状态与证据）

### 问题 1 [P0/P1，已修]：`e2e/cmd/dblatency` 在客户端断开或 `ctx` 超时/取消时泄漏后端 MySQL 连接与事务锁

- **位置**：[`e2e/cmd/dblatency/main.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/e2e/cmd/dblatency/main.go)
- **根因**：
  原实现中 `defer client.Close()` 和 `defer server.Close()` 写在 `wg.Wait()` 之后：
  ```go
  go func() { defer wg.Done(); copyDelayed(server, client, half) }()
  go func() { defer wg.Done(); copyDelayed(client, server, half) }()
  wg.Wait()
  ```
  当客户端关闭连接（例如 `e2e/run.sh` 执行 `kill -TERM "$WRITER"`，或 `go-sql-driver/mysql` 在 `ctx` 超时/取消时直接关闭底层 TCP 连接以中止在途请求），`copyDelayed(server, client)` 读到 `EOF` 退出；但此时 MySQL 服务端处于 `Sleep` 等待下一条命令的状态，`copyDelayed(client, server)` 永久阻塞在 `server.Read(buf)`，导致 `wg.Wait()` 永不返回、`server.Close()` 永不调用。
- **危害**：
  1. 在 `e2e/run.sh` 跑完后，被 `kill -TERM` 终止的在线 writer 留下的未提交事务永久滞留在 MySQL 中，持有 `reconcile_t2a` 表的 `SHARED_WRITE` MDL 锁和 InnoDB 行锁，导致后续重跑 `e2e` 执行 `DROP TABLE IF EXISTS old_orders` 时永久卡死在 `Waiting for table metadata lock`。
  2. 在 `mysqltable_it` 中任何使用 `ctx` 超时中断查询的测试都会导致连接泄漏。
- **现场证据（MySQL `SHOW FULL PROCESSLIST` & `information_schema.innodb_trx` 实测抓包）**：
  ```text
  id    user  host            db            command  time  state                             info
  1361  root  127.0.0.1:59482 reconcile_t2a Sleep    1022  NULL                              NULL
  1363  root  127.0.0.1:59485 reconcile_t2a Sleep    1022  NULL                              NULL
  2670  root  127.0.0.1:61509 reconcile_t2a Query    19    Waiting for table metadata lock   DROP TABLE IF EXISTS old_orders

  trx_id   trx_state  trx_started          trx_mysql_thread_id
  4064101  RUNNING    2026-04-10 02:13:01  1363
  4064099  RUNNING    2026-04-10 02:13:01  1361
  ```
- **修复**：在 `proxyConn` 的两个转发 goroutine 内部各自 `defer server.Close()` 和 `defer client.Close()`，任一方向断开立即关闭对端 socket 唤醒阻塞的 `Read`。

---

### 问题 2 [P1，已修]：`mysqltable` 的 `Tx.Update` 与 `Tx.Insert` 未校验 `len(Schema.Values(row)) == len(Schema.Columns)`

- **位置**：[`mysqltable/pair.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/pair.go)、[`mysqltable/routing.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/routing.go)
- **根因与危害**：
  1. `Tx.Update` 直接用 `for i, col := range p.cfg.Schema.Columns { ... vals[i] }` 下标访问：
     - 当 `len(vals) < len(Columns)` 时，在**已持有双侧 `FOR UPDATE` 行锁的复核事务内**直接触发 `runtime error: index out of range` panic；
     - 当 `len(vals) > len(Columns)` 时，静默丢弃多余返回值且不报错。
  2. `Tx.Insert` 直接 `args = append(args, p.cfg.Schema.Values(rows[k])...)`：
     - 在单行插入时由 MySQL 报错，但在多行批量插入（`InsertChunk >= 2`）时，若两行的 `Values` 长度一长一短刚好凑齐 `n * len(Columns)`（例如第 1 行返回 3 个值、第 2 行返回 1 个值，总列数为 2），SQL 占位符数量恰好匹配，**第 1 行多出的列值会静默篡改第 2 行的主键和业务列**！
- **修复**：新增 `Pair.values(row)` 统一校验 `len(vals) == len(Columns)`，不匹配时返回包装了 `errInvalidValues` 的明确错误，且在 `IsTransient` 中确保该错误永不触发重试。

---

### 问题 3 [P1，已修]：`mysqltable` 的 `Tx.Insert` 切分前未按 `Schema.KeyLess` 排序，在「老表分表 + 快照插入模式」下与在线写入并发触发真实 1213 死锁

- **位置**：[`mysqltable/pair.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/pair.go)
- **根因**：
  - `engine.repairBatch` 虽然对 `keys` 做了 `sortKeys`，但紧接着在 `InsertBatchSnapshot` 模式下：
    1. `tx.Lock(ctx, keys)` 对 `New` 表执行 `SELECT ... FOR UPDATE WHERE id IN (...)`，由于这些是 `missing` 行，在 MySQL `READ COMMITTED` 隔离级别下**不会对 `New` 表加任何锁**；
    2. 在非快照模式或老表也分表时，调用方传入 `Tx.Insert(ctx, toInsert)` 的 map 遍历后再按 `New.Route(k)` 分组，或直接调用 `Tx.Insert` 时，`byTable[tbl]` 内的 `ks` 未必保证按 `Schema.KeyLess` 升序排列（特别是老表分表时，`ScanOld` 先扫分片 0 的 key=2，再扫分片 1 的 key=1；若调用方或自定义 `KeyLess` 与分片顺序不一致，单分片 `Insert` 顺序就会乱序）。
  - 在 20ms 延迟下，单次多 chunk `INSERT` 事务跨多个 RTT 持锁时间达 60ms+，只要 `Tx.Insert` 先锁 key=2 再锁 key=1，而在线业务按升序先锁 key=1 再锁 key=2，就会触发 `Error 1213 (40001): Deadlock found when trying to get lock`。
- **修复**：在 `Tx.Insert` 对每个目标表 `tbl` 的 `ks` 切分 `InsertChunk` 之前，显式调用 `sort.Slice(ks, func(i, j int) bool { return p.cfg.Schema.KeyLess(ks[i], ks[j]) })`。

---

### 问题 4 [P2，已修]：`Route(k)` 越界返回不在 `Tables()` 中的表名时，`Lookup`/`Lock`/`Insert`/`Update` 未拦截，导致「幽灵写入 + 假阳性收敛」

- **位置**：[`mysqltable/pair.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/pair.go)、[`mysqltable/routing.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/routing.go)
- **根因与危害**：
  此前仅 `ScanPage` 检查了行是否归属当前扫描分片，而 `fetch`（被 `Lookup` 和 `Lock` 调用）、`Tx.Insert`、`Tx.Update` 直接相信 `r.Route(k)` 返回的字符串并拼入 SQL。
  当 `New.Route(k)` 存在 off-by-one（例如配置了 100 张分片表 `0..99`，但某键路由到 `shard_100`）：
  - 若 `shard_100` 在库中恰好存在（如历史遗留分片或预建分片），`Run` 会把数据插入 `shard_100` 并报告 `err == nil`；
  - 随后的核对扫描（`ScanNew`）只遍历 `Tables()`（`0..99`），永远扫不到 `shard_100`，但复核阶段 `Lookup` 又去 `shard_100` 查到了这行，导致重跑 `Run` 报告 `missing=0, extra=0, Consistent()=true`，形成**数据落入黑洞分片却报告完全一致**的严重静默故障。
- **修复**：新增 `checkRoute` / `checkRoutes`，在 `fetch`（`Lookup`/`Lock`）、`Tx.Insert`、`Tx.Update` 入口统一校验 `r.Route(k)` 必须在 `r.Tables()` 集合内（且非空），否则立即返回 `ErrMisrouted`（非瞬态错误，立即终止 `Run`）。

---

### 问题 5 [P2，已修]：`IsTransient` 与 `IsDuplicateKey` 被业务数据中的错误码/网络关键字子串欺骗

- **位置**：[`mysqltable/schema.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/schema.go)
- **根因与危害**：
  1. 原 `mysqlErrno` 使用 `strings.Index(s, "Error "+code)` 在整条错误文本中搜索。当 MySQL 返回 `Error 1062 (23000): Duplicate entry 'Error 1213: fake deadlock' for key 'PRIMARY'` 时，`mysqlErrno(err, "1213")` 返回 `true`，把主键冲突误判为死锁；反之 `Error 1205` 中若含 `'Error 1062'` 文本也会被误判为 `IsDuplicateKey`。
  2. 原 `IsTransient` 在 MySQL 错误码不是 `1213/1205/...` 时，继续执行 `strings.Contains(msg, "i/o timeout")` 等子串匹配。若业务主键含 `'i/o timeout'` 触发 `Error 1062`，或表名含 `'unexpected EOF'` 触发 `Error 1146`，会被误判为瞬态网络错误，在 20ms 延迟下白白重试 `MaxRetries` 次（每次浪费数十毫秒至数秒）。
- **修复**：新增 `firstMySQLErrno(s)` 仅解析第一个 `"Error <digits><delim>"` 头部的真实 MySQL 错误码；若已识别出 MySQL 服务端错误码但不在瞬态白名单（`1213, 1205, 1040, 1290, 1836`）内，直接返回 `false`，不再落入网络传输层子串匹配。

---

### 问题 6 [P2，已修]：`ScanPage` 在 `limit <= 0` 时越界 panic，且未校验跨侧（`Old` vs `New`）游标混用

- **位置**：[`mysqltable/pair.go`](file:///Users/chenquan/work/bakeoff/dbtable/t2/agy/mysqltable/pair.go)
- **根因与危害**：
  1. `ScanPage` 将 `limit` 直接传给 `LIMIT ?`；当调用方传入 `limit = 0` 时，MySQL 返回 0 行，`len(rows) == limit`（`0 == 0`）成立，随后执行 `rows[len(rows)-1]` 触发 `runtime error: index out of range [-1]` panic。
  2. `scanCursor` 结构体仅记录 `{shard int, lastKey Key}`，未绑定表名。若调用方误将 `New().ScanPage` 返回的游标（如 `shard=1`）传给单表 `Old().ScanPage`（`len(tables)==1`），类型断言通过且 `c.shard >= len(tables)`，会**静默返回 `nil, nil, nil`（假装扫描正常结束）**；若 `shard=0` 则会用新表游标扫老表。
- **修复**：`ScanPage` 在 `limit <= 0` 时返回明确错误；`scanCursor` 增加 `name string` 字段记录当前分片表名，校验 `c.shard < len(tables) && c.name == tables[c.shard]`，不匹配时返回 `foreign cursor` 错误。

---

## 4. 明确未解决 / 未验证的项

1. **宿主机 3313 端口正在运行的 `dblatency` 进程本身未重启（遵守约束）**：
   - 我们修复了仓库内的 `e2e/cmd/dblatency/main.go` 并用单测验证通过；但根据任务规则「不要自己再起 dblatency 代理占端口」，宿主环境由主控启动的 `127.0.0.1:3313` 进程仍是旧二进制。若在外部强杀持有未提交事务的客户端，3313 上的旧进程仍可能残留后端连接（我们在测试中通过等待事务自然回滚/提交避免了触发旧进程泄漏）。
2. **`mysqltable` 分片扫描与分片 `Lookup`/`Lock` 的串行 RTT 放大（架构设计观察，未改产品语义）**：
   - 在 20ms RTT 下，`TestRunConvergesWithOddSizes` 耗时 56s、`e2e/run.sh`（ROWS=2000）耗时 165s，核心原因是 `Pair.fetch` 对按分片分组后的 `byTable` 顺序逐表执行 `SELECT`，且 `ScanNew` 对 100 个分片顺序逐个扫描（仅扫空分片就要 `100 × 20ms = 2s`）。因为并发化 `Lookup` 或改变事务内 `Lock` 顺序涉及连接池占用与跨分片加锁顺序语义变更，遵守「产品代码只改证明得了的真 bug、改动保持最小」原则，未做激进并发改造。
3. **超大行数（如 `ROWS=50000 DIST=skew`）在 20ms 延迟下的全量 E2E 未跑**：
   - 实测 `ROWS=2000` 在 20ms RTT 下跑完 3 轮修复耗时 `2m45s`；按线性外推，`ROWS=50000` 在 20ms 延迟下单次 E2E 需要 60 分钟以上，超出本轮 50 分钟时限，故标记为**未验证**。

---

## 5. 最终门禁复跑结果、总耗时与核心结论

### 5.1 最终门禁复跑

- `./scripts/check.sh`（含全部新增单测 + `dblatency` 单测 + `vet` + `gofmt` + `bash -n`）：**PASS**（约 6s）
- `E2E_MYSQL_DSN="root@tcp(127.0.0.1:3313)/reconcile_t2a" ./scripts/check.sh integration`（含原有 11 个 + 新增 6 个共 17 个真实 MySQL 20ms 延迟集成测试，无 Skip）：**PASS（`87.773s`）**

### 5.2 本轮自测总耗时

- 约 **34 分钟**（含基线 unit/integration/e2e 执行、MySQL 现场锁泄漏排查、6 个方向代码深挖、12 个测试编写、回退变红取证与全量回归）。

### 5.3 如果只允许带一条结论回主干，是哪条？

> **在跨可用区（20ms RTT）延迟下，任何持锁窗口和连接生命周期缺陷都会被放大一个数量级以上：产品侧必须在进入/持有事务锁的边界上做强不变量校验与确定性排序（`Tx.Insert` 按 `KeyLess` 排序防死锁、`Insert/Update` 校验 `len(Values)==len(Columns)` 防持锁 panic 与多行错位写、`Lookup/Lock/Insert/Update` 校验 `Route(k) ∈ Tables()` 防幽灵分片假收敛），同时测试基础设施（`dblatency`）必须在任一端 EOF 时立即双向关闭连接，否则一次 `ctx` 超时或客户端 SIGTERM 就会在 MySQL 上留下永久持有行锁与 MDL 的僵尸事务。**

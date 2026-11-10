# 测试清单与覆盖口径 / Test scope and coverage

验证日期：2026-11-09（v0.3.x 各节）；v0.4.0 的 20ms 延迟验证为 2026-11-10。命令、真实输出和失败记录统一见 [VERIFICATION](../VERIFICATION.md)。当前业务执行代码保持不变；新增的是契约回归测试和发布检查入口。

## 关键路径 / Critical paths

| 路径 | 现有或新增测试 | 本轮证据 |
|---|---|---|
| Missing 插入、Different 整行覆盖、Extra 保留 | TestFixPolicyInsertsOverwritesKeepsExtra（三种模式），TestWarnPolicyNeverWrites | 无 DB race 通过 |
| 并发候选过期、重读最新老值 | TestStaleCandidateIsSkipped，TestRecheckUsesFreshOldValue | 无 DB race 通过；不能替代真实数据库锁验证 |
| 提交响应丢失后重试 / 重启 | 新增 TestLostCommitResponsePreservesData（6 子场景）、TestLostCommitResponseCanRecoverOnNextRun（3 子场景） | 行值与固定期望一致、Extra 不删、新 Run 无重复写；race 通过 |
| 页/批跨界、分组、内存估算阈值 | TestMemoryLimitFlushesEarly，TestBatchSpansPages，TestTransactionsSplitByGroupAndSize | 无 DB race 通过 |
| 小规模 quickstart 分布与全量边界不缩水 | 新增 TestQuickstartUniformAndSkewBoundary，TestDefaultSkewPreservesFrozenPageBoundaries | e2e store 无 DB race 验证，保留原脚本默认值 |
| 重试耗尽、查询部分失败不能视为缺失、取消 | TestEveryStepIsRetried，TestRetriesExhaustedStopsRun，TestLookupErrorNeverMeansMissing，TestCancelIsNotRetried | 无 DB race 通过 |
| 已提交整批计数与通知失败 | TestNotifyFailureCountsWholeCommittedBatch / AllConfirmedExtras / DoesNotCountUncommittedChunks | 无 DB race 通过，明确通知不是可靠投递 |
| 错片、锁前排序、超大整数和比较器边界 | mysqltable/regression_test.go，TestWithinInt64Boundaries，TestStructRejectsPromotedFieldConfiguration | 无 DB race 通过，SQL 层使用自定义 database/sql 测试驱动 |
| 真正的行锁、SQL 事务回滚、主键冲突与高水位、Values 逐行校验、路由越界、Insert 排序防死锁 | e2e/mysqltable_it（v0.4.0 起 17+ 个顶层测试） | v0.4.0 在 20ms 代理下的实跑结果见 VERIFICATION 第 11 节 |
| 并发双写、三轮收敛、独立逐行核验 | e2e/run.sh → writer / reconcile / verify | 2000 行 uniform、30000 行 skew 均三轮收敛并通过独立逐行校验 |

## 延迟注入与共享集成库 / Latency injection and the shared IT database

v0.4.0 起所有发布验证都在 20ms RTT 下跑（见 VERIFICATION 第 11 节）。两种用法：

**(a) 已有外部延迟代理（不自建代理）**：DSN 直接指向代理端口即可，`LATENCY_MS` 不设。例如主控在 3315 上提供 20ms 代理：

```sh
export E2E_MYSQL_DSN="root@tcp(127.0.0.1:3315)/reconcile_v040"
./scripts/check.sh integration
ROWS=2000 USERS=1000 DIST=uniform WARMUP=2 GAP=3 ./e2e/run.sh
```

**(b) 由 e2e/run.sh 自建代理**：设置 `LATENCY_MS=<整数毫秒>`（可选 `LATENCY_PORT`，默认 3307）。脚本构建 `e2e/bin/dblatency`，监听 `127.0.0.1:$LATENCY_PORT`、转发到 DSN 里的 `tcp(host:port)`，然后把 DSN 的地址改写为代理地址，writer / reconcile / verify 全部经代理；日志在 `e2e/logs/dblatency.log`，脚本退出时杀掉代理。DSN 必须是 `tcp(host:port)` 形式，否则退出码 2。

```sh
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" LATENCY_MS=20 LATENCY_PORT=3307 \
  DIST=uniform ROWS=2000 USERS=1000 WARMUP=2 GAP=3 ./e2e/run.sh
```

集成测试不经 run.sh，需要手动起代理再把 DSN 指过去：

```sh
(cd e2e && go run ./cmd/dblatency -listen 127.0.0.1:3307 -target 127.0.0.1:3306 -rtt 20ms) &
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3307)/reconcile_codex" ./scripts/check.sh integration
```

`dblatency` 每个方向延迟 `rtt/2`（`-jitter` 可加随机抖动），且**任一方向断开即关闭两端**：客户端 ctx 取消关闭 socket 时，MySQL 端会话同步断开、事务回滚。不要用不传播关闭的代理做取消类测试，否则服务端会残留持锁事务直到 `wait_timeout`（真实生产的半开连接同理，见 operations）。

**`reconcile_it` 与 GET_LOCK**：`e2e/mysqltable_it` 无视 DSN 里的库名，固定在共享库 `reconcile_it` 上 DROP/重建 `it_old`、`it_new_*`。`TestMain` 在专用连接上 `SELECT GET_LOCK('db_table_fulfill_reconcile_it_suite', 900)`，整个包跑完才释放（进程崩溃时随连接自动释放）。因此同一实例上可以并发启动多个 suite，它们会排队串行执行。语义：

- MySQL 连不上 → 不加锁，各测试自行 Skip（`check.sh integration` 把 Skip 判为门禁失败）；
- 连上了但 `GET_LOCK` 出错或 900s 内拿不到 → **直接失败退出**（v0.4.0 起；之前是不加锁继续跑，会与别的 suite 互相破坏表）。

## 覆盖率 / Coverage

以下覆盖率按本节给出的命令在 v0.3.0 上测量，可复现。

根 module 的可复现命令：`go test -race -count=1 -coverpkg=./... -coverprofile=/tmp/cover.out ./... && go tool cover -func=/tmp/cover.out | tail -1`（下表合计 86.2% 即该命令 `total:` 行）。分母是根 module 三个包的**可执行语句**，不是行、分支、场景或生产流量覆盖。`-coverpkg=./...` 将根包测试调用 memtable 的执行也计入；仅看默认逐包 go test 会低估 memtable 的使用覆盖。多个测试二进制的同一位置按覆盖集合合并，不把各行百分比相加。

| 包 / Package | 已执行 / 总语句 | 覆盖率 |
|---|---|---|
| reconcile 根包 | 376 / 393 | 95.7% |
| memtable | 85 / 93 | 91.4% |
| mysqltable | 188 / 259 | 72.6% |
| 合计 | 649 / 745 | 87.1% |

`go tool cover -func` 的合计为 87.1%（相对 v0.2.0 提升 0.9pp，来自冲突重做/退避取消用例与 v0.3.0 的路由校验用例）。基线默认逐包口径为根包 94.1%、memtable 49.5%、mysqltable 69.1%；它与上表的跨包口径不同，不用二者直接计算“提升幅度”。e2e 是独立 module，未包含在上述分母；新增 store 数据分布测试不代表覆盖 CLI、数据库写入或 Chaos，CLI 仍无原有测试。

## 未覆盖分支与风险 / Gaps and risk

- **覆盖分母边界**：`mysqltable.InTx`、Lock、Insert、Update 在根 module 的覆盖报告中为 0%；独立 e2e module 的真实 MySQL 集成/e2e 已通过，但不计入该覆盖率分母。根包的 memtable 事务通过本身不证明 MySQL 隔离级别、网络、锁或驱动行为。远程 CI 已执行：run 42596345410，Go 1.22.x / 1.25.4 双 job 均通过。
- **高风险、需要独立故障演练**：实际 COMMIT 响应丢失、数据库故障转移、进程/磁盘故障尚未注入。新增 mock 只在真实 memtable 提交之后返回错误，验证引擎恢复契约；现有 Chaos 只模拟 SQL 前或 COMMIT 前失败，不能代表实际网络断连。
- **中风险**：ScanPage 57.6%，未覆盖全部查询/解码错误、游标与空/尾页组合；根模块 SQL 测试驱动不是数据库。本轮真实分页、高水位集成测试与 30000 行 skew 已通过，包含 999/1000/1001 等固定分布；仍不等于穷尽分页与错误组合。
- **已补测（v0.2.2）**：回退期间的取消（`TestCancelDuringRetryBackoffStopsImmediately`）、快照插入的冲突重做轮次（`TestPersistentConflictIsBoundedAndReported` / `TestConflictRecoversWithinRounds`）现已覆盖；`retry.retryable` 达 100%，`retry.do` 94.4%。
- **中风险**：仍缺双端同时失败、Lock/Insert 在事务中途失败（已写入后回滚）的组合。现有失败及回滚测试保护主要契约，但不等于完整故障矩阵。
- **较低风险**：自定义 RouterFunc 路径、memtable.Delete、部分枚举默认 String、无操作回调和永久错误 Unwrap 分支未覆盖；分别涉及自定义路由接入、测试工具及诊断文字。字符串主键 collation 和业务自定义 adapter 仍需业务方专项验证。
- race 检测只覆盖本次测试中执行到的并发；引擎本身串行，不证明用户回调、双写方或多个独立任务之间没有竞争。

不为提高数字删测、Skip、削弱断言或修改生产逻辑。暂不设任意覆盖率阈值，以数据面断言、真实数据库验证和已解释的未覆盖风险共同评审。

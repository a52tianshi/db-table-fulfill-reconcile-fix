# MySQL 端到端测试

独立 Go module（通过 `replace ../` 引用本库），MySQL 驱动只在这里引入，根模块仍零依赖。

被测对象就是库本身：`cmd/e2e/reconcile.go` 用 `mysqltable.NewPair` + `reconcile.Run` 接入，`internal/store` 只有表结构、`Schema` 映射、分表规则、雪花 ID 和数据分布。writer（模拟线上双写）与 verify（独立逐行比对）不依赖库。

## 两类测试

| 测试 | 运行 | 覆盖 |
|---|---|---|
| `mysqltable_it/` 集成测试 | `go test ./...` | 库的 MySQL 实现：空表和页边界上的分页扫描、同一游标重复请求、扫描上限、雪花大整数 ID、按分表路由的批量查询、分片批量插入、主键冲突映射为 `ErrConflict`、回滚、整行更新、行锁确实生效、跨 IN 分块的统一锁序、正确 Extra 保留报警、错片/跨批副本显式错误、高水位不是时间快照，以及用互不整除的小参数完整跑两轮。MySQL 连不上时自动跳过 |
| `run.sh` 端到端测试 | `./run.sh` | 下面的 6 步流程：持续双写、三轮核对、停机逐行比对 |

## 模型

- 老表 `orders_old` 1 张，新表 `orders_new_00`…`orders_new_99` 100 张，结构相同，按 `user_id % 100` 分表。
- `order_id`（雪花算法，多 worker 大致递增）为主键；`order_id`、`user_id` 不可变；`status/amount/remark/version/updated_at` 可变。

## 数据分布（默认 `DIST=skew`）

scan 分页与引擎批次默认都是 1000。`skew` 分布专门覆盖不均匀的情况：

| 分片 | 情况 |
|---|---|
| 00–04 | 始终为空（writer 也不往这里插入） |
| 05–09 | 初始为空，writer 运行中才插入 |
| 10–16、18 | 冻结在 1 / 2 / 999 / 1000 / 1001 / 1999 / 2000 / 2001 行（不足一页、正好一页、跨页边界）；writer 只更新不插入 |
| 17 | 热点分片，约占 30% |
| 其余 | 均匀分布 |

verify 会检查冻结分片的行数仍是预期值，证明这些边界情况确实存在过。`DIST=uniform` 切换为均匀分布。

## 缺失行的插入方式（`INSERT_MODE`）

| 值 | 做法 | 第二轮断言 |
|---|---|---|
| `batch-snapshot`（默认） | 按分片、每 `TX_BATCH` 个 key 一个事务，直接用扫描时的值多行 INSERT。最快；扫描后被更新的行会以旧值写入 | 无缺失/额外；允许覆盖若干"不同"（就是这些旧值） |
| `batch-locked` | 按分片、每 `TX_BATCH` 个 key 一个事务，`FOR UPDATE` 重读老表后多行 INSERT | 0 修复 |
| `per-row` | 逐条事务复核插入 | 0 修复 |

"内容不同"和"仅新表存在"不受插入方式影响：总是按分片批量加锁复核，确认"不同"后逐条 UPDATE。三种方式都要求第三轮在写入仍进行时 0 修复。

批量快照插入能靠下一轮收敛，前提是：老表**没有删除**（否则被删的行会以旧值插入新表，之后只会报警为额外行、永不删除），且下一轮的"不同"走加锁重读的逐条修复。

## 流程（`./run.sh`）

| 步骤 | 命令 | 断言 |
|---|---|---|
| 1 建表 + 随机写老表，新表为空 | `e2e init` | — |
| 2 后台持续新增/修改老表，同事务双写新表，直到第 5 步 | `e2e writer` | 进程存活 |
| 3 第一轮核对修复 | `e2e reconcile -expect-repairs` | 有修复（大量插入） |
| 4 等待后第二轮（写入仍在进行） | `e2e reconcile -expect-clean`（snapshot 模式为 `-expect-no-missing`） | 见上表 |
| 4b 第三轮（写入仍在进行） | `e2e reconcile -expect-clean` | 复核后确认的 缺失/不同/额外 和修复数均为 0 |
| 5 停止所有写入 | `kill -TERM writer` | writer 正常退出 |
| 6 在同一一致性快照内逐行比对 | `e2e verify` | 新表每行都在正确分片、老表每行在新表存在且全列相等、总数相等 |

第 4 步的 `suspected` 可以大于 0：扫描老表和查新表之间，这一行如果被双写更新了，第一次比对会疑似不同，事务内重读后又相等，计入 `stale`。这是预期行为，不算失败。

双写语义：模拟线上代码，`UPDATE` 新表时若该行尚未迁移则影响 0 行，交由框架补齐。写入方与库的 Repairer 都按 老表 → 新表 的顺序加行锁，使用 READ COMMITTED。**写入方本身不重试**：死锁/锁等待超时会让该事务回滚并计入 `failed`（库侧的 Repairer 才按 `Retry` 策略重试）。

## 运行

首次运行优先看根目录 [Quickstart](../README.md#0-五分钟运行--quickstart)：已有本机 MySQL 时先建专属库，再显式传 DSN。`ROWS=2000` 时必须用 `DIST=uniform`；默认 skew 的固定分页边界需要至少 27009 行。缩小参数仅用于快速验证，全量脚本默认值未改。不要在共享环境使用下面示例中的默认 `reconcile_e2e` 库。

```sh
cd e2e
./run.sh                                     # 默认本机 root@tcp(127.0.0.1:3306)/reconcile_e2e
ROWS=200000 WORKERS=16 GAP=20 ./run.sh       # 加大规模
PAGE=97 BATCH=250 TX_BATCH=17 MEMORY_LIMIT=8000 FAIL_RATE=0.05 ./run.sh  # 互不整除的小参数 + 内存上限 + 5% 故障
INIT_ARGS="-copy-ratio 0.5 -dirty-ratio 0.3" ./run.sh   # 预置部分新表数据(含旧值)，覆盖"不同→覆盖"分支
E2E_MYSQL_DSN='user:pass@tcp(host:3306)/db' ./run.sh

# 单独使用子命令（参数见 bin/e2e <cmd> -h）
bin/e2e reconcile -missing warn -different warn      # 只告警
bin/e2e reconcile -insert batch-locked -tx-batch 100 -memory-limit 1048576 -ignore UpdatedAt
bin/e2e reconcile -fail-rate 1 -retry-attempts 3     # 每次调用都失败: 重试 3 次后 Run 退出
bin/e2e reconcile -progress-every 500ms              # 进度日志间隔（0 = 不输出进度）
```

`run.sh` 默认 `FAIL_RATE=0.02`：在扫描每一页、批量查询、事务开始、提交前以及通知回调前，按 2% 概率注入一个伪造的可重试错误（`1213` 为主，另有 `1205`/`2006`/`2013`，见 `internal/store/chaos.go`），验证每一步都会被重试。运行输出按阶段给出计数：`injected=N (scan=… lookup=… txBegin=… txPreCommit=… notify=…)`。报告中 `retries` = `injected` + MySQL 真实发生的可重试错误（死锁等，实测一直是 0），所以两者应当相等。设为 `FAIL_RATE=0` 可以关闭注入。

`init` 会 **DROP 并重建** DSN 指定库中的上述 101 张表。writer 日志在 `logs/writer.log`。

发布检查入口是根目录 `./scripts/check.sh integration`：保留现有测试逻辑，但将数据库不可用导致的 Skip 判为门禁失败。`mysqltable_it` 无论传什么 DSN 都使用共享 `reconcile_it`；同实例上不要并发跑。当前执行记录和 mock 边界见 [VERIFICATION](../VERIFICATION.md) 与 [故障注入说明](../docs/operations.md#故障注入--fault-injection)，本页下方是既有历史参考结果，不能替代本轮验证。

## 参考结果

本机 Apple M5，MySQL 9.7.1，默认参数（`ROWS=50000`、`DIST=skew`、8 个写入线程、页/批 1000、事务 200 个 key），`GAP=3`。每组都是一次完整的 `./run.sh`，全部 `E2E PASS`。

| `INSERT_MODE` | `FAIL_RATE` | 第一轮 | 第二轮覆盖旧值 | 第三轮修复 | 重试 = 注入 |
|---|---|---|---|---|---|
| `batch-snapshot` | 0 | 5.5s | 82 行 | 0 | — |
| `batch-snapshot` | 0.02 | 6.6s | 92 行 | 0 | 188+8+10，全部相等 |
| `batch-locked` | 0 | 7.7s | 0 | 0 | — |
| `batch-locked` | 0.02 | 13.7s | 0 | 0 | 193+12+9，全部相等 |
| `per-row` | 0 | 53s | 0 | 0 | — |
| `per-row` | 0.02 | 62s | 0 | 0 | 1984+26+26，全部相等 |

其他组合（同样全部通过）：

| 参数 | 第一轮 | 说明 |
|---|---|---|
| `INSERT_MODE=batch-locked PAGE=97 BATCH=250 TX_BATCH=17 MEMORY_LIMIT=8000 FAIL_RATE=0.05` | 46s | 页、批、事务大小互不整除，内存上限生效，第一轮重试 2537 = 注入 2537 |
| `DIST=uniform FAIL_RATE=0.05` | 9.7s | 100 张分表均匀分布，第二轮覆盖 145 行旧值 |

几点解读：

- 第二轮覆盖的行数，取决于扫描和插入之间有多少行恰好被线上改动，每次运行都不一样。只有 `batch-snapshot` 会出现，另两种一次就写对。
- 注入故障的成本主要是重试前的等待（`-retry-backoff` 默认 5ms，按次翻倍）。`per-row` 第一轮有 5 万多个事务，注入次数最多，所以变慢最明显。
- 页、批设得很小时明显变慢（46s 对比约 8s），因为 SQL 条数多了。生产环境建议保持在 1000 左右。

## 注意事项

- `mysqltable_it` 在 MySQL 连不上时会 Skip，验证时请用 `go test -v` 确认没有被跳过，不要把 Skip 当成通过。
- 集成测试会重建 `reconcile_it` 库里的测试表，完整 e2e 会 DROP DSN 指向的库里的测试表，请使用专用库或隔离实例。

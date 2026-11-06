# MySQL 端到端测试

独立 Go module（通过 `replace ../` 引用框架），MySQL 驱动只在这里引入，根模块仍零依赖。

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
| `batch-snapshot`（默认） | 每个分片一条多行 INSERT，直接用扫描时的值。最快；扫描后被更新的行会以旧值写入 | 无缺失/额外；允许覆盖若干"不同"（就是这些旧值） |
| `batch-locked` | 每个分片一个事务，`FOR UPDATE` 重读老表后多行 INSERT | 0 修复 |
| `row` | 逐条事务复核插入 | 0 修复 |

"内容不同"和"仅新表存在"始终逐条复核。三种方式都要求第三轮在写入仍进行时 0 修复。

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

第 4 步的 `candidates` 可以大于 0：扫描老表与查新表之间若被双写更新，初比会疑似不同，事务内重读后相等，计为 `staleSkipped`。这是预期行为，不算失败。

双写语义：模拟线上代码，`UPDATE` 新表时若该行尚未迁移则影响 0 行，交由框架补齐。写入方与 Reviewer 都按 老表 → 新表 的顺序加行锁，使用 READ COMMITTED，死锁/锁等待超时会重试。

## 运行

```sh
cd e2e
./run.sh                                     # 默认本机 root@tcp(127.0.0.1:3306)/reconcile_e2e
ROWS=200000 WORKERS=16 GAP=20 ./run.sh       # 加大规模
INIT_ARGS="-copy-ratio 0.5 -dirty-ratio 0.3" ./run.sh   # 预置部分新表数据(含旧值)，覆盖"不同→覆盖"分支
E2E_MYSQL_DSN='user:pass@tcp(host:3306)/db' ./run.sh
```

`init` 会 **DROP 并重建** DSN 指定库中的上述 101 张表。writer 日志在 `logs/writer.log`。

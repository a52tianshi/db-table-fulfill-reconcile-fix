# 验证记录 / Verification

日期：2026-11-09（Asia/Singapore）。分支 `codex/clarity`，基线 HEAD `74c845f287eb1e5c55947e5e8a343dcd63c50f37`。不切分支、不 merge、不 push；已尝试提交；文件系统禁止创建 worktree 的 index.lock，未能暂存或提交。

**总体验收：fail。** 提交因 `.git` 只读失败；用户原样指定的 2000 行命令使用默认 skew，因 `skew plan needs -rows >= 27009` 退出 1。该失败保留，不能将后续修正参数后的成功写成原命令通过。已有文档要求的小规模 `DIST=uniform` 复跑通过；30000 行默认 skew、真实 MySQL 集成、build/vet/race 均通过。未发现需要修改实现的缺陷，没有更改默认分布或削弱边界断言。

上一轮受限，本轮已复跑：MySQL socket/TCP 连接成功，旧的沙箱权限失败记录由以下本轮真实输出替换。Go 命令统一先设置 `export GOCACHE=/tmp/codex-clarity-go-build`，使用允许写入的缓存路径；未改依赖版本。用户提供环境为 Go 1.25.4 darwin/arm64、MySQL 9.7.1、root 无密码。未安装工具或 Docker，也未重新探测版本。

除明确写出 `cd e2e` 的命令外，工作目录为仓库根目录。输出中的随机种子、流量、注入次数和耗时随运行变化。

## 1. 建库

```sh
mysql -u root -e "CREATE DATABASE IF NOT EXISTS reconcile_codex"
```

退出码 0。stdout/stderr 均为空。

## 2. 用户要求的根模块检查

```sh
go build ./... && go vet ./... && go test -race -count=1 ./...
```

退出码 0。真实输出：

```text
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix	1.554s
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable	2.135s
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable	2.684s
```

build 和 vet 成功时没有 stdout/stderr；上方是随后 race 测试的全部输出。

## 3. 原样 2000 行命令：fail

```sh
cd e2e
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" ROWS=2000 USERS=1000 WARMUP=2 GAP=3 ./run.sh
```

退出码 1。真实输出：

```text

==== 1. 建表并随机写入老表 (2000 行, 新表 100 张为空) ====
[init] FAIL: skew plan needs -rows >= 27009
```

这是已有 skew 数据计划的输入约束，非连接错误。该计划保留空片、热点片和 999/1000/1001 等冻结分页边界；已有 `TestQuickstartUniformAndSkewBoundary` 明确断言 2000 行 skew 必须拒绝。没有为让原命令变绿而缩小边界或改变默认值。

## 4. 2000 行 uniform：pass

```sh
cd e2e
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" DIST=uniform ROWS=2000 USERS=1000 WARMUP=2 GAP=3 ./run.sh
```

退出码 0。真实输出：

```text

==== 1. 建表并随机写入老表 (2000 行, 新表 100 张为空) ====
[init] seed=1794214565979737000 old=2000 users=1000 preCopied=0 preDirty=0 in 371ms
[init] dist=uniform

==== 2. 启动线上模拟: 持续 新增/修改 老表 + 事务双写新表 ====
[writer] started workers=8 known=2000 insertRatio=0.30

==== 3. 第一轮核对修复 (期望大量插入) ====
reconcile progress phase=old batch=1 pages=1 scanned old=1000 new=0 | missing=1000 different=0 extra=0 | inserted=1000 updated=0 warned=0 | retries=6 | 0s | running
reconcile progress phase=old batch=7 pages=7 scanned old=6666 new=0 | missing=2000 different=0 extra=0 | inserted=2000 updated=0 warned=0 | retries=16 | 0s | phase done
reconcile progress phase=new batch=9 pages=100 scanned old=6666 new=8058 | missing=2000 different=0 extra=0 | inserted=2000 updated=0 warned=0 | retries=18 | 1s | phase done
reconcile progress phase=new batch=9 pages=100 scanned old=6666 new=8058 | missing=2000 different=0 extra=0 | inserted=2000 updated=0 warned=0 | retries=18 | 1s | done
[run1] insert=batch-snapshot injected=18 scanned old=6666 new=8058 suspected=2046 stale=46 | missing=2000 different=0 extra=0 | inserted=2000 updated=0 warned=0 | retries=18 542ms

==== 4. 等待 3s 后第二轮 (-expect-no-missing, 写入仍在进行) ====
reconcile progress phase=old batch=1 pages=1 scanned old=1000 new=0 | missing=0 different=8 extra=0 | inserted=0 updated=8 warned=0 | retries=1 | 0s | running
reconcile progress phase=old batch=17 pages=17 scanned old=16990 new=0 | missing=0 different=19 extra=0 | inserted=0 updated=19 warned=0 | retries=8 | 1s | phase done
reconcile progress phase=new batch=19 pages=100 scanned old=16990 new=18729 | missing=0 different=19 extra=0 | inserted=0 updated=19 warned=0 | retries=14 | 1s | phase done
reconcile progress phase=new batch=19 pages=100 scanned old=16990 new=18729 | missing=0 different=19 extra=0 | inserted=0 updated=19 warned=0 | retries=14 | 1s | done
[run2] insert=batch-snapshot injected=14 scanned old=16990 new=18729 suspected=96 stale=77 | missing=0 different=19 extra=0 | inserted=0 updated=19 warned=0 | retries=14 704ms
[run2] PASS: no missing/extra; 19 stale snapshot rows overwritten

==== 4b. 第三轮 (必须 0 修复, 写入仍在进行: 证明已收敛) ====
reconcile progress phase=old batch=1 pages=1 scanned old=1000 new=0 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=0 | 0s | running
reconcile progress phase=old batch=19 pages=19 scanned old=18984 new=0 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=1 | 1s | phase done
reconcile progress phase=new batch=21 pages=100 scanned old=18984 new=20561 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=5 | 1s | phase done
reconcile progress phase=new batch=21 pages=100 scanned old=18984 new=20561 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=5 | 1s | done
[run3] insert=batch-snapshot injected=5 scanned old=18984 new=20561 suspected=62 stale=62 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=5 681ms
[run3] PASS: nothing to repair (62 stale candidates resolved by reread)

==== 5. 停机: 停止所有写入 ====
[writer] stopped inserts=18829 updates=43798 failed=0

==== 6. 一致性快照内逐行比对 ====
[verify] new shard sizes: empty=0 below1000=100 min=162 max=250
[verify] old=20829 new=20829 checked=20829 missing=0 different=0 misrouted=0 extra=0 | 123ms
[verify] PASS: old and new tables are identical

E2E PASS
```

## 5. 30000 行默认 skew：pass

```sh
cd e2e
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" ROWS=30000 USERS=1000 WARMUP=2 GAP=3 ./run.sh
```

退出码 0。真实输出：

```text

==== 1. 建表并随机写入老表 (30000 行, 新表 100 张为空) ====
[init] seed=1794214582329029000 old=30000 users=1000 preCopied=0 preDirty=0 in 544ms
[init] dist=skew
  orders_new_00: always empty
  orders_new_01: always empty
  orders_new_02: always empty
  orders_new_03: always empty
  orders_new_04: always empty
  orders_new_05: empty at init, writer fills
  orders_new_06: empty at init, writer fills
  orders_new_07: empty at init, writer fills
  orders_new_08: empty at init, writer fills
  orders_new_09: empty at init, writer fills
  orders_new_10: frozen 1 rows
  orders_new_11: frozen 2 rows
  orders_new_12: frozen 999 rows
  orders_new_13: frozen 1000 rows
  orders_new_14: frozen 1001 rows
  orders_new_15: frozen 1999 rows
  orders_new_16: frozen 2000 rows
  orders_new_17: hot shard (~30%)
  orders_new_18: frozen 2001 rows

==== 2. 启动线上模拟: 持续 新增/修改 老表 + 事务双写新表 ====
[writer] started workers=8 known=30000 insertRatio=0.30

==== 3. 第一轮核对修复 (期望大量插入) ====
reconcile progress phase=old batch=1 pages=1 scanned old=1000 new=0 | missing=1000 different=0 extra=0 | inserted=1000 updated=0 warned=0 | retries=2 | 0s | running
reconcile progress phase=old batch=17 pages=17 scanned old=17000 new=0 | missing=17000 different=0 extra=0 | inserted=17000 updated=0 warned=0 | retries=68 | 2s | running
reconcile progress phase=old batch=34 pages=34 scanned old=33753 new=0 | missing=30000 different=0 extra=0 | inserted=30000 updated=0 warned=0 | retries=109 | 4s | phase done
reconcile progress phase=new batch=44 pages=112 scanned old=33753 new=43586 | missing=30000 different=0 extra=0 | inserted=30000 updated=0 warned=0 | retries=112 | 4s | phase done
reconcile progress phase=new batch=44 pages=112 scanned old=33753 new=43586 | missing=30000 different=0 extra=0 | inserted=30000 updated=0 warned=0 | retries=112 | 4s | done
[run1] insert=batch-snapshot injected=112 scanned old=33753 new=43586 suspected=30001 stale=1 | missing=30000 different=0 extra=0 | inserted=30000 updated=0 warned=0 | retries=112 4.288s

==== 4. 等待 3s 后第二轮 (-expect-no-missing, 写入仍在进行) ====
reconcile progress phase=old batch=1 pages=1 scanned old=1000 new=0 | missing=0 different=3 extra=0 | inserted=0 updated=3 warned=0 | retries=0 | 0s | running
reconcile progress phase=old batch=53 pages=53 scanned old=52916 new=0 | missing=0 different=134 extra=0 | inserted=0 updated=134 warned=0 | retries=5 | 1s | phase done
reconcile progress phase=new batch=58 pages=116 scanned old=52916 new=57069 | missing=0 different=134 extra=0 | inserted=0 updated=134 warned=0 | retries=11 | 2s | phase done
reconcile progress phase=new batch=58 pages=116 scanned old=52916 new=57069 | missing=0 different=134 extra=0 | inserted=0 updated=134 warned=0 | retries=11 | 2s | done
[run2] insert=batch-snapshot injected=11 scanned old=52916 new=57069 suspected=184 stale=50 | missing=0 different=134 extra=0 | inserted=0 updated=134 warned=0 | retries=11 1.875s
[run2] PASS: no missing/extra; 134 stale snapshot rows overwritten

==== 4b. 第三轮 (必须 0 修复, 写入仍在进行: 证明已收敛) ====
reconcile progress phase=old batch=1 pages=1 scanned old=1000 new=0 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=0 | 0s | running
reconcile progress phase=old batch=58 pages=58 scanned old=57661 new=0 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=8 | 1s | phase done
reconcile progress phase=new batch=62 pages=118 scanned old=57661 new=61796 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=8 | 2s | phase done
reconcile progress phase=new batch=62 pages=118 scanned old=57661 new=61796 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=8 | 2s | done
[run3] insert=batch-snapshot injected=8 scanned old=57661 new=61796 suspected=56 stale=56 | missing=0 different=0 extra=0 | inserted=0 updated=0 warned=0 | retries=8 1.841s
[run3] PASS: nothing to repair (56 stale candidates resolved by reread)

==== 5. 停机: 停止所有写入 ====
[writer] stopped inserts=32329 updates=75103 failed=0

==== 6. 一致性快照内逐行比对 ====
[verify] new shard sizes: empty=5 below1000=89 min=0 max=16555
[verify] old=62329 new=62329 checked=62329 missing=0 different=0 misrouted=0 extra=0 | 305ms
[verify] PASS: old and new tables are identical

E2E PASS
```

两次均使用脚本原有默认 `FAIL_RATE=0.02`、`INSERT_MODE=batch-snapshot`，未关闭故障注入。uniform 最终校验 20829 行；skew 最终校验 62329 行，均为 missing/different/misrouted/extra = 0，writer failed=0，第三轮 0 修复。30000 是初始行数，并发新增使最终行数增加。两次 e2e 串行执行，只重建专属 `reconcile_codex` 的测试表。

## 6. 真实 MySQL 集成门禁：pass

```sh
E2E_MYSQL_DSN="root@tcp(127.0.0.1:3306)/reconcile_codex" ./scripts/check.sh integration
```

退出码 0。真实输出：

```text
=== RUN   TestScanPageUnevenShards
--- PASS: TestScanPageUnevenShards (0.05s)
=== RUN   TestScanStopsAtHighWaterMark
--- PASS: TestScanStopsAtHighWaterMark (0.04s)
=== RUN   TestLookupRoutesAndConfirmsAbsence
--- PASS: TestLookupRoutesAndConfirmsAbsence (0.04s)
=== RUN   TestTxInsertUpdateConflictRollback
--- PASS: TestTxInsertUpdateConflictRollback (0.04s)
=== RUN   TestLockHoldsRowLocks
--- PASS: TestLockHoldsRowLocks (1.96s)
=== RUN   TestRunConvergesWithOddSizes
--- PASS: TestRunConvergesWithOddSizes (0.32s)
=== RUN   TestWrongShardStopsRunWithoutDeleting
=== RUN   TestWrongShardStopsRunWithoutDeleting/extra
=== RUN   TestWrongShardStopsRunWithoutDeleting/duplicate
--- PASS: TestWrongShardStopsRunWithoutDeleting (0.07s)
    --- PASS: TestWrongShardStopsRunWithoutDeleting/extra (0.04s)
    --- PASS: TestWrongShardStopsRunWithoutDeleting/duplicate (0.04s)
=== RUN   TestCorrectlyRoutedExtraWarnsAndRemains
--- PASS: TestCorrectlyRoutedExtraWarnsAndRemains (0.04s)
=== RUN   TestLockSortsAcrossChunksAgainstOnlineWriter
--- PASS: TestLockSortsAcrossChunksAgainstOnlineWriter (0.04s)
=== RUN   TestHighWaterIsKeyBoundNotTemporalSnapshot
--- PASS: TestHighWaterIsKeyBoundNotTemporalSnapshot (0.04s)
=== RUN   TestWrongShardInLockRollsBackEarlierUpdate
--- PASS: TestWrongShardInLockRollsBackEarlierUpdate (0.04s)
PASS
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/mysqltable_it	4.230s
```

11 个顶层测试及错片的 2 个子测试均实际通过，无 Skip。集成测试内部仍按既有实现使用共享 `reconcile_it`；仅启动了一次集成命令，没有并发运行其他共享该库的测试，没有重试或修改测试。

## 7. 双模块完整本地检查：pass

```sh
./scripts/check.sh all
```

退出码 0。真实输出：

```text
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix	1.619s
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/memtable	1.809s
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/mysqltable	1.287s
ok  	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/internal/store	1.627s
?   	github.com/a52tianshi/db-table-fulfill-reconcile-fix/e2e/cmd/e2e	[no test files]
lint PASS (gofmt + bash -n)
```

脚本分别对根 module 和 e2e module 执行 build/vet，根 module 与 e2e/internal、e2e/cmd 执行 race 单测，再检查 gofmt 和 shell 语法。CLI 的 `[no test files]` 不算测试覆盖，lint 不包含 staticcheck。

## 8. 覆盖口径与证据边界

上一轮根 module 跨包语句覆盖为 617/716 = 86.2%（核心 373/393、memtable 85/93、mysqltable 159/230）；本轮没有重跑覆盖率，不将这组数字伪称本轮新测量。该分母不含独立 e2e module，根 module 中 MySQL 事务函数的 0% 也不表示本轮集成没有执行它们。完整口径和未覆盖风险见 [docs/testing.md](docs/testing.md)。

- 新增 memtable 提交后丢响应回归只证明 Repairer 契约；真实网络丢 COMMIT 响应、主从故障转移、进程/磁盘故障未演练。
- 本轮跑了 30000 行 skew；未跑原默认 50000 行/20000 用户、5% 故障矩阵或生产容量演练。
- CI 尚未 push 或远程运行；Go 1.22.x / MySQL 8.4 配置不能当作已通过兼容矩阵。
- 库现有的通知交付、持久化检查点、跨实例事务和全局快照能力边界未改变。
- 本轮只更新验证证据及与其直接矛盾的文档状态，没有重写实现、改 API、改 go.mod/go.sum、删除或弱化测试。

## 9. Diff 检查与提交失败

`git diff --check` 退出码 0，无输出。另检查全部改动文件（含未跟踪文件）的尾部空白和换行。不将上轮独立评审描述为本轮新评审；go.mod/go.sum 两个 module 均未改动。

提交的第一步：

```sh
git add -A
```

退出码 128，真实输出：

```text
fatal: Unable to create '/Users/chenquan/work/bakeoff/dbtable/main/.git/worktrees/codex/index.lock': Operation not permitted
```

当前环境虽然放开了网络，但 `.git` 仍只读，且审批策略不允许提权。暂存失败，未执行 `git commit`，新增提交 0；所有改动留在 `codex/clarity` 工作区。初次批处理在 add 失败后仍继续了 cached diff 检查；这些空暂存区检查不作为交付证据。随后单独运行 add 确认退出码 128，以上是其完整输出。没有切分支、merge、push 或通过其他方式绕过文件系统权限。

提交未完成是另一项总体验收 fail 的原因，需要在允许写入仓库 Git 元数据的环境完成暂存和提交。未超出 45 分钟时限；这是权限阻塞，并非超时停止。

# 测试清单与覆盖口径 / Test scope and coverage

验证日期：2026-11-09。命令、真实输出和失败记录统一见 [VERIFICATION](../VERIFICATION.md)。当前业务执行代码保持不变；新增的是契约回归测试和发布检查入口。

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
| 真正的行锁、SQL 事务回滚、主键冲突与高水位 | e2e/mysqltable_it 中 11 个测试 | 本轮 11 个顶层测试及 2 个子测试通过，无 Skip |
| 并发双写、三轮收敛、独立逐行核验 | e2e/run.sh → writer / reconcile / verify | 2000 行 uniform、30000 行 skew 均三轮收敛并通过独立逐行校验 |

## 覆盖率 / Coverage

以下覆盖率为上一轮测量，本轮复跑 build/vet/race 与真实数据库验证，没有重新测量覆盖率。

根 module 使用 `go test -race -count=1 -coverpkg=./... -coverprofile=... ./...`。分母是根 module 三个包的**可执行语句**，不是行、分支、场景或生产流量覆盖。`-coverpkg=./...` 将根包测试调用 memtable 的执行也计入；仅看默认逐包 go test 会低估 memtable 的使用覆盖。多个测试二进制的同一位置按覆盖集合合并，不把各行百分比相加。

| 包 / Package | 已执行 / 总语句 | 覆盖率 |
|---|---|---|
| reconcile 根包 | 373 / 393 | 94.9% |
| memtable | 85 / 93 | 91.4% |
| mysqltable | 159 / 230 | 69.1% |
| 合计 | 617 / 716 | 86.2% |

`go tool cover -func` 的合计为 86.2%。基线默认逐包口径为根包 94.1%、memtable 49.5%、mysqltable 69.1%；它与上表的跨包口径不同，不用二者直接计算“提升幅度”。e2e 是独立 module，未包含在上述分母；新增 store 数据分布测试不代表覆盖 CLI、数据库写入或 Chaos，CLI 仍无原有测试。

## 未覆盖分支与风险 / Gaps and risk

- **覆盖分母边界**：上一轮 `mysqltable.InTx`、Lock、Insert、Update 在根 module 覆盖报告为 0%；本轮独立 e2e module 的真实 MySQL 集成/e2e 已通过，但不计入该覆盖率分母。根包的 memtable 事务通过本身不证明 MySQL 隔离级别、网络、锁或驱动行为。远程 CI 未执行。
- **高风险、需要独立故障演练**：实际 COMMIT 响应丢失、数据库故障转移、进程/磁盘故障尚未注入。新增 mock 只在真实 memtable 提交之后返回错误，验证引擎恢复契约；现有 Chaos 只模拟 SQL 前或 COMMIT 前失败，不能代表实际网络断连。
- **中风险**：ScanPage 57.6%，未覆盖全部查询/解码错误、游标与空/尾页组合；根模块 SQL 测试驱动不是数据库。本轮真实分页、高水位集成测试与 30000 行 skew 已通过，包含 999/1000/1001 等固定分布；仍不等于穷尽分页与错误组合。
- **中风险**：根包 recheckTx 88.6%、retry.do 83.3%，仍有缺失双端、Lock/Insert 中途失败、取消恰逢退避等待等组合未覆盖。现有失败及回滚测试保护主要契约，但不等于完整故障矩阵。
- **较低风险**：自定义 RouterFunc 路径、memtable.Delete、部分枚举默认 String、无操作回调和永久错误 Unwrap 分支未覆盖；分别涉及自定义路由接入、测试工具及诊断文字。字符串主键 collation 和业务自定义 adapter 仍需业务方专项验证。
- race 检测只覆盖本次测试中执行到的并发；引擎本身串行，不证明用户回调、双写方或多个独立任务之间没有竞争。

不为提高数字删测、Skip、削弱断言或修改生产逻辑。暂不设任意覆盖率阈值，以数据面断言、真实数据库验证和已解释的未覆盖风险共同评审。

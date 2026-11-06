#!/usr/bin/env bash
# End-to-end flow:
#   1 init old table   2 start dual writer   3 reconcile (many repairs)
#   4 reconcile again (must be clean)   5 stop all writes   6 row-by-row verify
set -euo pipefail
cd "$(dirname "$0")"

ROWS=${ROWS:-50000}          # old-table rows in step 1
USERS=${USERS:-20000}
WORKERS=${WORKERS:-8}         # dual-writer goroutines
INSERT_RATIO=${INSERT_RATIO:-0.3}
WARMUP=${WARMUP:-3}           # seconds of traffic before step 3
GAP=${GAP:-10}                # seconds between step 3 and step 4
BATCH=${BATCH:-1000}
PAGE=${PAGE:-1000}           # keyset scan page for old and new tables
DIST=${DIST:-skew}            # skew | uniform
INSERT_MODE=${INSERT_MODE:-batch-snapshot}  # row | batch-locked | batch-snapshot
export E2E_MYSQL_DSN=${E2E_MYSQL_DSN:-"root@tcp(127.0.0.1:3306)/reconcile_e2e"}

mkdir -p bin logs
go build -o bin/e2e ./cmd/e2e
step() { printf '\n==== %s ====\n' "$*"; }

step "1. 建表并随机写入老表 ($ROWS 行, 新表 100 张为空)"
bin/e2e init -dist "$DIST" -rows "$ROWS" -users "$USERS" ${INIT_ARGS:-}

step "2. 启动线上模拟: 持续 新增/修改 老表 + 事务双写新表"
bin/e2e writer -dist "$DIST" -workers "$WORKERS" -insert-ratio "$INSERT_RATIO" -users "$USERS" > logs/writer.log 2>&1 &
WRITER=$!
trap 'kill -TERM $WRITER 2>/dev/null || true' EXIT
sleep "$WARMUP"
kill -0 "$WRITER" || { cat logs/writer.log; echo "writer died"; exit 1; }
tail -n 2 logs/writer.log

step "3. 第一轮核对修复 (期望大量插入)"
bin/e2e reconcile -label run1 -insert "$INSERT_MODE" -batch "$BATCH" -page "$PAGE" -expect-repairs

# batch-snapshot may leave rows stale (updated between scan and insert); run2
# must then overwrite them (different>0 allowed) but find nothing missing.
if [ "$INSERT_MODE" = batch-snapshot ]; then RUN2_EXPECT=-expect-no-missing; else RUN2_EXPECT=-expect-clean; fi
step "4. 等待 ${GAP}s 后第二轮 ($RUN2_EXPECT, 写入仍在进行)"
sleep "$GAP"
kill -0 "$WRITER" || { cat logs/writer.log; echo "writer died"; exit 1; }
bin/e2e reconcile -label run2 -insert "$INSERT_MODE" -batch "$BATCH" -page "$PAGE" $RUN2_EXPECT

step "4b. 第三轮 (必须 0 修复, 写入仍在进行: 证明已收敛)"
kill -0 "$WRITER" || { cat logs/writer.log; echo "writer died"; exit 1; }
bin/e2e reconcile -label run3 -insert "$INSERT_MODE" -batch "$BATCH" -page "$PAGE" -expect-clean

step "5. 停机: 停止所有写入"
kill -TERM "$WRITER"
wait "$WRITER"
trap - EXIT
tail -n 1 logs/writer.log

step "6. 一致性快照内逐行比对"
bin/e2e verify -dist "$DIST"

printf '\nE2E PASS\n'

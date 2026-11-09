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
TX_BATCH=${TX_BATCH:-200}     # keys per recheck / insert transaction
MEMORY_LIMIT=${MEMORY_LIMIT:--1}  # bytes buffered per batch (-1 = none)
PAGE=${PAGE:-1000}           # keyset scan page for old and new tables
DIST=${DIST:-skew}            # skew | uniform
INSERT_MODE=${INSERT_MODE:-batch-snapshot}  # per-row | batch-locked | batch-snapshot
FAIL_RATE=${FAIL_RATE:-0.02}  # injected transient failures per scan/lookup/tx call (exercises Retry)
export E2E_MYSQL_DSN=${E2E_MYSQL_DSN:-"root@tcp(127.0.0.1:3306)/reconcile_e2e"}

mkdir -p bin logs
go build -o bin/e2e ./cmd/e2e
step() { printf '\n==== %s ====\n' "$*"; }

CLEANUP_PIDS=()
cleanup() {
  local pid
  for pid in ${CLEANUP_PIDS[@]+"${CLEANUP_PIDS[@]}"}; do kill -TERM "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT

# LATENCY_MS simulates a production network: every DB round trip pays extra
# latency. The whole flow (writer, reconcile, verify) then goes through the
# proxy, so timeouts, lock waits and retry paths see realistic RTTs.
if [ -n "${LATENCY_MS:-}" ]; then
  case "$LATENCY_MS" in
    ''|*[!0-9]*) echo "LATENCY_MS must be an integer number of milliseconds" >&2; exit 2 ;;
  esac
  if [ "$LATENCY_MS" -gt 0 ]; then
    LATENCY_PORT=${LATENCY_PORT:-3307}
    DB_ADDR=$(printf '%s' "$E2E_MYSQL_DSN" | sed -nE 's/.*tcp\(([^)]*)\).*/\1/p')
    if [ -z "$DB_ADDR" ]; then
      echo "LATENCY_MS is set but E2E_MYSQL_DSN is not a tcp(host:port) DSN" >&2
      exit 2
    fi
    go build -o bin/dblatency ./cmd/dblatency
    bin/dblatency -listen "127.0.0.1:$LATENCY_PORT" -target "$DB_ADDR" -rtt "${LATENCY_MS}ms" \
      > logs/dblatency.log 2>&1 &
    LATENCY_PID=$!
    CLEANUP_PIDS+=("$LATENCY_PID")
    sleep 0.3
    kill -0 "$LATENCY_PID" || { cat logs/dblatency.log; echo "dblatency died"; exit 1; }
    E2E_MYSQL_DSN=$(printf '%s' "$E2E_MYSQL_DSN" | sed -E "s#\(tcp\)\([^)]*\)#(tcp)(127.0.0.1:$LATENCY_PORT)#")
    export E2E_MYSQL_DSN
    printf 'latency: +%sms RTT via 127.0.0.1:%s -> %s\n' "$LATENCY_MS" "$LATENCY_PORT" "$DB_ADDR"
    printf 'DSN: %s\n' "$E2E_MYSQL_DSN"
  fi
fi

step "1. 建表并随机写入老表 ($ROWS 行, 新表 100 张为空)"
bin/e2e init -dist "$DIST" -rows "$ROWS" -users "$USERS" ${INIT_ARGS:-}

step "2. 启动线上模拟: 持续 新增/修改 老表 + 事务双写新表"
bin/e2e writer -dist "$DIST" -workers "$WORKERS" -insert-ratio "$INSERT_RATIO" -users "$USERS" > logs/writer.log 2>&1 &
WRITER=$!
CLEANUP_PIDS+=("$WRITER")
sleep "$WARMUP"
kill -0 "$WRITER" || { cat logs/writer.log; echo "writer died"; exit 1; }
tail -n 2 logs/writer.log

step "3. 第一轮核对修复 (期望大量插入)"
bin/e2e reconcile -label run1 -insert "$INSERT_MODE" -batch "$BATCH" -page "$PAGE" -tx-batch "$TX_BATCH" -memory-limit "$MEMORY_LIMIT" -fail-rate "$FAIL_RATE" -expect-repairs

# batch-snapshot may leave rows stale (updated between scan and insert); run2
# must then overwrite them (different>0 allowed) but find nothing missing.
if [ "$INSERT_MODE" = batch-snapshot ]; then RUN2_EXPECT=-expect-no-missing; else RUN2_EXPECT=-expect-clean; fi
step "4. 等待 ${GAP}s 后第二轮 ($RUN2_EXPECT, 写入仍在进行)"
sleep "$GAP"
kill -0 "$WRITER" || { cat logs/writer.log; echo "writer died"; exit 1; }
bin/e2e reconcile -label run2 -insert "$INSERT_MODE" -batch "$BATCH" -page "$PAGE" -tx-batch "$TX_BATCH" -memory-limit "$MEMORY_LIMIT" -fail-rate "$FAIL_RATE" $RUN2_EXPECT

step "4b. 第三轮 (必须 0 修复, 写入仍在进行: 证明已收敛)"
kill -0 "$WRITER" || { cat logs/writer.log; echo "writer died"; exit 1; }
bin/e2e reconcile -label run3 -insert "$INSERT_MODE" -batch "$BATCH" -page "$PAGE" -tx-batch "$TX_BATCH" -memory-limit "$MEMORY_LIMIT" -fail-rate "$FAIL_RATE" -expect-clean

step "5. 停机: 停止所有写入"
kill -TERM "$WRITER"
wait "$WRITER"
tail -n 1 logs/writer.log

step "6. 一致性快照内逐行比对"
bin/e2e verify -dist "$DIST"

printf '\nE2E PASS\n'

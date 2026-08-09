#!/usr/bin/env bash
# outbox のバックログ推移を一定間隔でサンプリングし、CSV に記録する。
#
# 負荷試験（とくに make load/points）と併走させ、非同期化した guild 集計が
# 生産レートに追いつけているかを判定するための計測スクリプト。
# API のレイテンシ（k6 が測る）だけでは「非同期側に積み残していないか」が見えないため、
# バックログを直接観測する。
#
# 計測自体が負荷にならないよう、docker exec は**計測全体で1回だけ**起動し、
# 常駐した mysql クライアントの標準入力にクエリを流し込む方式を採る。
# （サンプリング毎に docker exec すると Docker Desktop for Mac では無視できない
#   オーバーヘッドになり、観測が対象を歪める）
#
# 使い方:
#   make load/watch                                  # 既定 900 秒
#   make load/watch WATCH_OUT=run1.csv WATCH_DURATION=600
#   ./loadtest/watch_outbox.sh out.csv 600 1
#
# 出力列:
#   elapsed_sec       計測開始からの経過秒（MySQL 側の時刻を基準にするため出力遅延の影響を受けない）
#   pending           未処理イベント数（= guild_scores に未反映の加算）
#   processed_total   処理済みイベント累計
#   processed_delta   サンプリング間隔あたりの消化数（= worker の実スループット）
#   guild_scores_sum  guild_scores.score の総和（反映の進捗）
#
# 読み方:
#   - 負荷中に pending が単調増加   → worker が生産レートに追いつかない
#   - processed_delta が頭打ち      → その値が worker のスループット上限
#   - 負荷終了後に pending が減らず平坦
#                                   → 通知が尽き、OUTBOX_POLL_INTERVAL 待ちに入っている
#                                     （既定 10m。runOnce は1回あたり OUTBOX_BATCH_SIZE 件で返る）
set -euo pipefail

OUT=${1:-${WATCH_OUT:-outbox_metrics.csv}}
DURATION=${2:-${WATCH_DURATION:-900}}
INTERVAL=${3:-${WATCH_INTERVAL:-1}}
CONTAINER=${MYSQL_CONTAINER:-game-api-mysql}
DB_NAME=${MYSQL_DATABASE:-game_db}
DB_USER=${MYSQL_USER:-game}
DB_PASS=${MYSQL_PASSWORD:-game}

if ! docker inspect "$CONTAINER" >/dev/null 2>&1; then
  echo "error: mysql container '$CONTAINER' not found." >&2
  echo "hint: docker compose -f deployments/docker-compose.yml up -d" >&2
  exit 1
fi

# 1サンプル分のクエリ。時刻を MySQL 側で採ることで、クライアント側の出力バッファリングで
# 表示が遅れても elapsed_sec の正確さが保たれる。
# SUM(条件) は該当行なしで NULL を返すため 0 に丸める。
readonly SQL="SELECT
  UNIX_TIMESTAMP(NOW(3)),
  COALESCE(SUM(processed_at IS NULL), 0),
  COALESCE(SUM(processed_at IS NOT NULL), 0),
  (SELECT COALESCE(SUM(score), 0) FROM guild_scores)
FROM outbox_events;"

# INTERVAL 秒ごとにクエリを1本ずつ標準出力へ流す。DURATION 経過で終了し、
# パイプが閉じることで mysql クライアントも終了する。
feed_queries() {
  local start elapsed
  start=$(date +%s)
  while :; do
    elapsed=$(( $(date +%s) - start ))
    [ "$elapsed" -ge "$DURATION" ] && break
    printf '%s\n' "$SQL"
    sleep "$INTERVAL"
  done
}

echo "watching outbox backlog: out=$OUT duration=${DURATION}s interval=${INTERVAL}s" >&2

feed_queries \
  | docker exec -i -e MYSQL_PWD="$DB_PASS" "$CONTAINER" \
      mysql -N -B -u "$DB_USER" "$DB_NAME" \
  | awk -F'\t' -v OFS=, '
      BEGIN { print "elapsed_sec,pending,processed_total,processed_delta,guild_scores_sum" }
      NF < 4 { next }
      {
        if (t0 == "") t0 = $1
        delta = (prev == "") ? 0 : $3 - prev
        prev = $3
        print int($1 - t0), $2, $3, delta, $4
        fflush()
      }
    ' \
  | tee "$OUT"

echo "done: $OUT" >&2

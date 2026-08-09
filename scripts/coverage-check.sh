#!/usr/bin/env bash
# 層別カバレッジ（文カバレッジ）が閾値を満たすかを判定する。
#
# AGENTS.md §3 の層別カバレッジ目標を「表示するだけ」から「未達で CI が落ちる」へ格上げする
# ためのスクリプト。未達時は、そのまま改善ループ（go-testing-qa スキルの
# references/tdd-workflow.md §11）に入れるよう、未カバー箇所の一覧と分類の観点を出力する。
#
# 使い方:
#   scripts/coverage-check.sh [coverage.out のパス]
#   （既定は ./coverage.out。`make test/race` または `make test/cover` が生成する）
set -euo pipefail

PROFILE="${1:-coverage.out}"

# ---- 層別閾値（文カバレッジ %）------------------------------------------------
#
# 正本は AGENTS.md §3。ここはその実行可能な写しなので、片方だけ変えない。
#
# 【値の根拠と履歴】
#   internal/domain          100  外部依存が無く 100% が達成可能な層のため。導入時の実測も 100.0%
#                                 （2026-08-09 に 90 から引き上げ。それ以前は AGENTS.md §3 の初期値）
#   internal/usecase          90  導入時の実測 97.6%（同日 85 から引き上げ）
#   internal/driver           90  §3 に目標が無かったため新設。導入時の実測 96.9%
#   internal/infrastructure   80  AGENTS.md §3。実リソースを起動しない方針のため上げ幅に限界がある
#   configs                   90  環境変数パースのみで外部依存が無い層。導入時の実測 100.0%
#                                 （2026-08-09 新設。それ以前は閾値なしで 75.5%。未カバーだった
#                                  OUTBOX_TICK_TIMEOUT / DB_PING_TIMEOUT のバリデーションに
#                                  テストを追加して 100% にしてから閾値を設定した）
#
# 【閾値を決めるときの注意】
# 層が小さいほど 1 文あたりのポイントが大きく、閾値の粒度が粗くなる。
# 実測との差（スラック）が 1 文未満の閾値は「一切の変化を許さない」設定であり、
# 次の1行追加で落ちる。閾値を決める前に「1 文 = 何ポイントか」を確認すること
# （本スクリプトの出力に文数が併記してある）。
#
# 引き上げるときは AGENTS.md §3 と併せて更新し、ここに日付と理由を追記すること。
# 下げる場合は、目標を下げたのか責務の置き場所を変えたのかを明記する
# （references/layered-architecture-testing.md §11）。
# 区切りは ";"（macOS の awk は -v の値に改行を含められないため）
THRESHOLDS="internal/domain=100;internal/usecase=90;internal/driver=90;internal/infrastructure=80;configs=90"

# ---- 入力チェック -------------------------------------------------------------
if [ ! -f "$PROFILE" ]; then
  echo "エラー: カバレッジプロファイルが見つかりません: $PROFILE" >&2
  echo "  先に 'make test/race'（race 検出あり）か 'make test/cover' を実行してください。" >&2
  exit 1
fi

# 古いプロファイルで判定すると「通ったのに実は未計測」が起きるため、
# プロファイルより新しい .go があれば警告する（判定自体は続行する）。
newer=$(find . -name '*.go' -newer "$PROFILE" -not -path './bin/*' -print -quit 2>/dev/null || true)
if [ -n "$newer" ]; then
  echo "警告: $PROFILE より新しい .go ファイルがあります（例: $newer）。"
  echo "      計測をやり直す場合は 'make test/race' を実行してください。"
  echo
fi

# ---- 集計と判定 ---------------------------------------------------------------
# coverage.out の各行は「<パス>:<開始>,<終了> <文数> <実行回数>」。
# 文カバレッジ = 実行回数>0 の文数 / 全文数。go tool cover -func の total と一致する。
awk -v thresholds="$THRESHOLDS" '
BEGIN {
  n = split(thresholds, lines, ";")
  for (i = 1; i <= n; i++) {
    split(lines[i], kv, "=")
    thr[kv[1]] = kv[2] + 0
    order[i] = kv[1]
  }
  order_len = n
}

function layer_of(path,   l) {
  for (i = 1; i <= order_len; i++) {
    l = order[i]
    if (index(path, l "/") == 1) return l
  }
  return "(閾値なし) " path_pkg(path)
}

# 閾値の無いパスは、どのパッケージかが分かる粒度でまとめる
function path_pkg(path,   parts, m, out, j) {
  m = split(path, parts, "/")
  out = parts[1]
  for (j = 2; j < m; j++) out = out "/" parts[j]
  return out
}

NR > 1 {
  path = $0
  sub(/:.*/, "", path)
  sub(/^github\.com\/uchidas-rogue\/game-api-sample\//, "", path)

  stmts = $(NF - 1) + 0
  count = $NF + 0
  l = layer_of(path)

  if (!(l in seen)) { seen[l] = 1; keys[++key_len] = l }
  total[l] += stmts
  if (count > 0) covered[l] += stmts

  if (count == 0) {
    loc = $0
    sub(/ [0-9]+ 0$/, "", loc)
    sub(/^github\.com\/uchidas-rogue\/game-api-sample\//, "", loc)
    uncovered[l] = uncovered[l] sprintf("    %s (%d 文)\n", loc, stmts)
  }

  grand_total += stmts
  if (count > 0) grand_covered += stmts
}

END {
  printf "層別カバレッジ（文カバレッジ）\n\n"
  # 全角は表示幅 2 桁なので、awk の %-Ns では桁が合わない。ヘッダは実測値の桁に合わせて直書きする。
  printf "  層                             実測      閾値  判定\n"
  printf "  --------------------------  -------  --------  ----\n"

  failed = 0

  # 閾値のある層を定義順に表示する
  for (i = 1; i <= order_len; i++) {
    l = order[i]
    if (!(l in total)) {
      printf "  %-26s %8s %8.0f%%  %s\n", l, "計測なし", thr[l], "SKIP"
      continue
    }
    pct = covered[l] * 100 / total[l]
    ok = (pct >= thr[l]) ? "OK" : "NG"
    if (ok == "NG") { failed = 1; fail_layers[l] = 1 }
    # 判定は丸めない実測値で行い、表示は切り捨てる。
    # %.1f の四捨五入だと 99.96% が "100.0%" と表示されて閾値 100% で NG になり、
    # 「100% なのに落ちた」ように見えるため。
    printf "  %-26s %7.1f%% %8.0f%%  %s  (%d/%d 文)\n", l, int(pct * 10) / 10, thr[l], ok, covered[l], total[l]
  }

  # 閾値の無いパッケージは参考値として出す（黙って見えなくしない）
  first_extra = 1
  for (i = 1; i <= key_len; i++) {
    l = keys[i]
    if (l in thr) continue
    if (first_extra) { printf "\n  参考値（閾値未設定。regression は検知されない）\n"; first_extra = 0 }
    pct = covered[l] * 100 / total[l]
    printf "  %-26s %7.1f%% %8s  %s  (%d/%d 文)\n", l, pct, "-", "-", covered[l], total[l]
  }

  printf "\n  %-26s %7.1f%%\n", "TOTAL", grand_covered * 100 / grand_total

  if (!failed) {
    printf "\n全ての層が閾値を満たしています。\n"
    exit 0
  }

  printf "\n閾値を下回った層があります。未カバー箇所は以下のとおりです。\n"
  for (l in fail_layers) {
    printf "\n  [%s]\n%s", l, uncovered[l]
  }

  printf "\n改善の進め方（go-testing-qa スキル references/tdd-workflow.md §11）:\n"
  printf "  1. 上の未カバー箇所を次の4つに分類する\n"
  printf "       (a) 通常の分岐（到達可能）        → テストケースを追加する\n"
  printf "       (b) エントリポイント関数本体等     → スキップ（設計上テスト対象外）\n"
  printf "       (c) デッドコード（到達不可能）     → スキップし、リファクタ候補として報告する\n"
  printf "       (d) 外部要因依存で再現困難         → 再現可能ならテスト追加、困難ならスキップして報告\n"
  printf "  2. (a) と判定したものだけテストを追加する。\n"
  printf "     未カバー行を実行させるためだけの不自然なモックを足さない（過剰モック禁止）。\n"
  printf "  3. 再計測する。試行回数には上限を設け（AGENTS.md §6 の 3 回連続ルールに準じる）、\n"
  printf "     上限に達したら打ち切り、未カバー箇所の内訳（行・分類・理由）を報告して終了する。\n"
  exit 1
}
' "$PROFILE"

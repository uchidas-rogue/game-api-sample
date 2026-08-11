#!/usr/bin/env bash
# 指示書（AI エージェント向けドキュメント）の SSoT 崩れを検出する。
#
# 目的:
#   規約の正本が1箇所に保たれているか、記述が実態と乖離していないかを機械判定する。
#   `.claude/hooks/remind-docs.sh`（Stop hook）が「コードを変えたのに資料が未更新」を
#   見るのに対し、本スクリプトはその裏返し ——「資料どうしが食い違っている /
#   資料が実態と食い違っている」—— を見る。
#
# 設計方針は docs/testing/principles/deterministic-verification.md に従う:
#   §1 レビューで毎回同じ指摘をしている規約は機械検証へ回す
#   §2 判断基準を有限の規則セットに落とし込む（個別の例外名をその場で作らない）
#   §3 字面マッチに頼る場合は、既知の誤検出・検出漏れを検査ごとに明文化する
#   §4 ホワイトリスト方式（原則禁止・許可されたものだけ例外）
#   §5 ERROR（実態と矛盾している）と WARN（将来腐る形をしている）を区別する
#   §7 ローカルと CI で同一のチェッカーを同一引数で実行できるようにする
#
# 使い方:
#   scripts/docs-ssot-check.sh            # 全チェック（ERROR があれば exit 1）
#   scripts/docs-ssot-check.sh --list     # AGENTS.md §3 の正本表を表示するだけ
#
# 検査を追加するときは、必ず「この検査が捕捉する既知の実例」をコメントに書くこと。
# 実例を伴わない検査は誤検出源になりやすく、いずれ無効化される。
#
# 検査は「特定の一文・特定の1ファイル」ではなく構造で対象を指定すること。
# 日本語の文をスクリプトに焼き込むと、文言を書き換えただけで検査が静かに消える。
#
# ---- ssot-assert ディレクティブ（本コメントが記法の正本）----------------------
#
# 「現時点では〜が無い」のような時点依存の記述は、実態が変われば嘘になる。
# 照合ルールをスクリプト側に持つと記述のたびにスクリプト編集が必要になるため、
# 記述の側に機械照合の条件を書く。検査3がこれを評価する。
#
#   <!-- ssot-assert: <動詞> <引数...> -->
#
# 対象の記述と**同じ行または直後の行**に置く（表の行は、行を割ると表が壊れるので同じ行に置く）。
#
#   absent-grep '<正規表現>' <path>... [--include=<glob>] [--exclude=<glob>]
#       指定パスに該当が「無い」こと。見つかったら ERROR
#   present-grep '<正規表現>' <path>... [--include=<glob>] [--exclude=<glob>]
#       指定パスに該当が「有る」こと。見つからなければ ERROR
#   path-exists <path>...   パスが存在すること
#   path-absent <path>...   パスが存在しないこと
#   manual '<理由>'
#       機械照合が原理的に不可能なもの。理由（必須）を添えて WARN を抑止する。
#       ホワイトリスト方式（§4）の「許可の理由を対応づける」に相当する
#
# 正規表現はシングルクォート必須（eval を使わずに安全に切り出すため）。
# glob はクォートせずに書く（例: --exclude=*_test.go）。
#
# 【既知の誤検出パターン（determ. §3）】
# absent-grep は字面マッチなので **コード内のコメントにも当たる**。
#   実例: outbox GC の導入時、「アプリで time.Now() を呼ばない」と説明したコメント自体が
#         absent-grep 'time\.Now\(\)' に引っかかり、規約を守っているのに ERROR になった。
#         sqlc は SQL のコメントを生成コードへ転記するため、queries/*.sql のコメントも対象。
#   回避策: 禁止 API を説明する文では、その API 名を字面で書かず意図で書く
#         （「time.Now() を呼ばない」→「アプリ側で現在時刻を取得しない」）。
#         AST で判定すればコメントを除外できるが、対象が「特定の識別子の不在」だけなので
#         現状は字面マッチのまま、この回避策で運用する。
#
# コードブロック（``` / ~~~）とインラインコード（`...`）の中に書いたものは
# 「記法の説明」とみなし、評価しない（検査 3a）。記法を解説する指示書のために必要な除外だが、
# 裏返すと **例示のつもりでコード表記に入れたディレクティブは黙って無視される**。
# 実際に効かせたいディレクティブはコード表記の外に置くこと。
#
# 例:
#   - `testcontainers-go` による実 DB/Redis テストは未導入。
#     <!-- ssot-assert: absent-grep 'testcontainers' go.mod -->
set -euo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo .)"

ERRORS=0
WARNS=0
err()   { printf "  \033[31m[ERROR]\033[0m %s\n" "$*"; ERRORS=$((ERRORS + 1)); }
warn()  { printf "  \033[33m[WARN]\033[0m  %s\n" "$*"; WARNS=$((WARNS + 1)); }
head_() { printf "\n\033[1m%s\033[0m\n" "$*"; }

# ---- 検査対象の集合 -----------------------------------------------------------
#
# 文書の性質ごとに、かけてよい検査が違う。一緒くたにすると誤検出だらけになるので、
# 「どの検査がどの集合を使うか」をここに集約する。
#
#   ALL_DOCS         リポジトリ内の md 全部（logs/ と terraform/ は対象外）
#                    → 検査1（リンク切れ）・検査3の ssot-assert 評価・検査5
#
#   THRESHOLD_SCOPE  ALL_DOCS - docs/testing/principles/
#                    → 検査2（閾値の写し）
#                    principles/ は言語・プロジェクト非依存の一般論として「80%・90%」を
#                    例示するため、実態照合すると誤検出になる。
#                    一方、閾値の二重管理は計画文書でも起きた（ROADMAP.md が domain 90% /
#                    usecase 85% と書いていた）ので、計画文書は対象に含める。
#
#   INSTRUCTION_DOCS ALL_DOCS - docs/testing/principles/ - ROADMAP.md - docs/plans/
#                    → 検査3の時点依存 WARN・検査4（チェックリスト写し）
#                    計画文書は「まだ無いもの」を書くのが仕事なので時点依存マーカーは正常で、
#                    チェックリストもタスク一覧であって正本の写しではない。
#                    principles/ を検査4から外す理由: docs/testing/README.md §6 と
#                    principles/tdd-workflow.md §8 の6項目は「本リポジトリの運用形 vs
#                    一般原則」の意図的な差分であり（README 側に「食い違ったら本ファイルを
#                    優先する」と明記済み）、恒久 WARN を作らないため。
ALL_DOCS=$(git ls-files '*.md' | grep -Ev '^(logs|terraform)/' | sort)
INSTRUCTION_DOCS=$(printf '%s\n' "$ALL_DOCS" \
  | grep -Ev '^docs/testing/principles/|^ROADMAP\.md$|^docs/plans/')
THRESHOLD_SCOPE=$(printf '%s\n' "$ALL_DOCS" | grep -Ev '^docs/testing/principles/')

# ---- 正本表 -------------------------------------------------------------------
#
# 正本表そのものを別ファイル（docs/ssot.yml 等）に切ると、正本表が二重管理になって
# 本末転倒。AGENTS.md §3 の索引表を唯一の宣言箇所とし、ここではそれをパースして使う。
ssot_index() {
  awk '/^# 3\. Testing Rules/, /^# 4\./' AGENTS.md \
    | { grep -E '^\| .+ \| .+ \|$' || true; } \
    | { grep -v '^| 問い' || true; } \
    | { grep -v '^| ---' || true; }
}

if [ "${1:-}" = "--list" ]; then
  head_ "AGENTS.md §3 の正本表"
  ssot_index | sed 's/^/  /'
  exit 0
fi

printf "指示書 SSoT チェック（指示書 %s / 全 md %s ファイル）\n" \
  "$(printf '%s\n' "$INSTRUCTION_DOCS" | wc -l | tr -d ' ')" \
  "$(printf '%s\n' "$ALL_DOCS" | wc -l | tr -d ' ')"

# ---- 1. リンク切れ ------------------------------------------------------------
#
# 捕捉する実例: principles/ の移動（.claude/skills/go-testing-qa/references/ →
#               docs/testing/principles/）で全参照元が壊れるケース。
head_ "1. リンク切れ（全 md）"
before=$ERRORS
links_tmp=$(mktemp)
for f in $ALL_DOCS; do
  d=$(dirname "$f")
  { grep -o ']([^)]*)' "$f" 2>/dev/null || true; } | sed 's/^](//;s/)$//' | while read -r link; do
    case "$link" in http*|\#*|"") continue ;; esac
    target=${link%%#*}
    [ -z "$target" ] && continue
    [ -e "$d/$target" ] || printf '%s\t%s\n' "$f" "$link"
  done
done > "$links_tmp"
while IFS=$'\t' read -r f link; do
  [ -z "${f:-}" ] && continue
  err "$f -> $link （リンク先が存在しない）"
done < "$links_tmp"
rm -f "$links_tmp"
[ "$ERRORS" -eq "$before" ] && echo "  OK"

# ---- 2. カバレッジ閾値の二重管理 ----------------------------------------------
#
# 捕捉する実例:
#   - ROADMAP.md が domain 90% / usecase 85% と書き、AGENTS.md §3 は 100% / 90% だった
#   - .claude/agents/test-engineer.md が「カバレッジ85%以上」「85% を下回る場合」と書いていた
#   - AGENTS.md §3 と scripts/coverage-check.sh の THRESHOLDS のずれ（将来）
head_ "2. カバレッジ閾値の二重管理"
before=$ERRORS

# 2a. AGENTS.md §3 ⇄ scripts/coverage-check.sh
#
# 層名をこのスクリプトに列挙すると、列挙漏れの層が「両辺とも欠落 → 一致扱い」で
# 素通りする（§3 に書いたがスクリプトに入れ忘れた層が検出できない）。
# そこで両辺とも「層名」を抽出し、キー集合ごと突合する。
#   - スクリプト側: THRESHOLDS のキー（internal/domain, configs …）の basename
#   - AGENTS.md 側: §3 の層バレット（  - `<層名>`）に現れる「カバレッジ NN%」
# AGENTS.md 側は太字（**90%**）に依存しない。装飾を変えただけで検出が消えないようにするため。
script_th=$(grep -E '^THRESHOLDS=' scripts/coverage-check.sh \
  | sed 's/^THRESHOLDS="//;s/"$//' | tr ';' '\n' \
  | { grep -E '=' || true; } \
  | sed 's|^.*/||' | sort)
agents_th=$(awk '
  /^# 3\. Testing Rules/ { inSec = 1 }
  /^# 4\./              { inSec = 0 }
  inSec && /^  - `[a-z]+`/ {
    layer = $0; sub(/^  - `/, "", layer); sub(/`.*/, "", layer)
    # 桁数は {2,3} のような区間指定にしない。mawk（Ubuntu / CI の既定 awk）は区間で
    # 最短一致したあと後戻りしないため、`カバレッジ **100%**` が「10」で確定して
    # 直後の `%` に届かず、domain 層の 100% だけが抽出から漏れていた。
    if (match($0, /カバレッジ[ 　*]*[0-9]+[ 　]*%/)) {
      pct = substr($0, RSTART, RLENGTH)
      gsub(/[^0-9]/, "", pct)
      print layer "=" pct
    }
  }' AGENTS.md | sort)

if [ "$script_th" != "$agents_th" ]; then
  err "AGENTS.md §3 と scripts/coverage-check.sh の閾値が一致しない（層の過不足も含む）"
  printf "          AGENTS.md §3      : %s\n" "$(echo "$agents_th" | tr '\n' ' ')"
  printf "          coverage-check.sh : %s\n" "$(echo "$script_th" | tr '\n' ' ')"
fi

# 2b. AGENTS.md 以外に閾値が宣言されていないか
#
# 「カバレッジ」と % の**近接**（同一節内・語順は問わない）で文脈を判定する。
# 以前は `[0-9]{2,3}%を下回` だけを見ていたため、カバレッジ以外の %
# （p95 レイテンシ・成功率）にも当たる形だった。逆に行全体を文脈にすると、
# 「レイテンシ p95 が 95% を下回る（カバレッジとは無関係）」のように
# 別文脈の % と「カバレッジ」が同居する行で誤検出する。
#
# 宣言と報告を分ける規則:
#   (1) 「宣言の形」であること
#       - カバレッジの直後に % （装飾のみ許容。助詞が挟まる「カバレッジは 97.6%」は報告の形）
#       - カバレッジ … NN% 以上 / 未満 / を下回る / を上回る（同一節内・カバレッジが先）
#       - NN% 以上 / 未満 … カバレッジ（語順反転）
#   (2) かつ報告の語（過去形・結果報告）を含まないこと
#
# 既知の誤検出と回避策:
#   docs/testing/ranking.md:195「いずれも文カバレッジ 100% のまま見逃されていた」は
#   (1) の1つ目に当たってしまうため、(2) の報告語（のまま・見逃）で除外している。
#   新たに誤検出が出た場合は、報告語を足すのではなく「宣言の形」の定義を見直すこと。
# 既知の検出漏れ: 英語表記（"coverage 90% or higher"）は拾わない。本リポジトリの
#   指示書は日本語で書く前提のため。英語の指示書を追加するなら形を足すこと。
#
# 「近接」に `[^。]{0,30}`（文の区切りをまたがない書き方）ではなく `.{0,30}` を使う理由:
# 多バイト文字を含む否定クラスは、選択肢（|）と組み合わせると macOS の BSD grep で
# 一致しないことがある。実測では「カバレッジが 85% を下回る」「90% 以上のカバレッジ」の
# 2行を取りこぼした（同じ選択肢を単体で使うと一致するため、多バイト文字の否定クラスと
# 選択肢・回数指定の組み合わせに起因すると見られる）。判定がローカルと CI で揺れる形は
# 避けるべきなので（§7）、否定クラスを使わない。30 は日本語で約10文字ぶんの目安。
DECL_FORM='カバレッジ[ 　*]*[0-9]{2,3}[ 　]*%'
DECL_FORM="$DECL_FORM"'|カバレッジ.{0,30}[0-9]{2,3}[ 　]*%[ 　]*(以上|未満|を(下|上)回)'
DECL_FORM="$DECL_FORM"'|[0-9]{2,3}[ 　]*%[ 　]*(以上|未満).{0,30}カバレッジ'
REPORT_WORDS='実測|計測|表示され|すり抜け|見逃|のまま|だった|であり|になった|満たしていた'
for f in $THRESHOLD_SCOPE; do
  [ "$f" = "AGENTS.md" ] && continue
  # pipefail が有効なので、段ごとに || true を付ける（該当なしの grep で落ちるため）
  hits=$({ grep -nE "$DECL_FORM" "$f" || true; } \
    | { grep -vE "$REPORT_WORDS" || true; })
  [ -z "$hits" ] && continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    err "$f:${line%%:*} 層別カバレッジ閾値の写し → AGENTS.md §3 へのリンクに置き換える"
  done <<EOF
$hits
EOF
done
[ "$ERRORS" -eq "$before" ] && echo "  OK"

# ---- 3. 時点依存記述の腐敗 ----------------------------------------------------
#
# SSoT 検査（同じ情報が2箇所にある）では捕まらない型。1箇所にしか無くても、
# 実態が変われば嘘になる記述を、実態から照合する。
#
# 捕捉する実例:
#   - SKILL.md の「docs/testing/ は未整備」（同一ブランチで整備済みだった）
#   - AGENTS.md の「Clock インターフェースの実装は存在しない」
#   - AGENTS.md の「testcontainers-go は未導入」
#
# 照合ルールは記述の側（ssot-assert ディレクティブ / 記法は本ファイル冒頭）に置く。
# 以前は「Clock インターフェースの実装は存在しない」等の日本語の文をこのスクリプトに
# 焼き込んでいたため、(a) 文言を書き換えると ERROR が静かに消え、(b) `grep -q
# 'テストは未導入' AGENTS.md` がファイル全体を見ていたので、別の「〜テストは未導入」行が
# 増えると無関係な行に対して testcontainers のメッセージが出る、という2つの弱点があった。
head_ "3. 時点依存記述の実態照合"
before=$ERRORS
beforew=$WARNS

# ssot-assert ディレクティブを1件評価する。 $1=ファイル $2=行番号 $3=本体("動詞 引数...")
eval_assert() {
  local f=$1 n=$2 body=$3
  local verb rest pattern tok hits
  local -a paths flags
  verb=${body%% *}
  rest=${body#"$verb"}
  rest=${rest# }
  case "$verb" in
    absent-grep|present-grep)
      case "$rest" in
        \'*) ;;
        *) err "$f:$n ssot-assert: 正規表現はシングルクォートで囲む（$body）"; return ;;
      esac
      rest=${rest#\'}
      case "$rest" in
        *\'*) ;;
        *) err "$f:$n ssot-assert: シングルクォートが閉じていない（$body）"; return ;;
      esac
      pattern=${rest%%\'*}
      rest=${rest#*\'}
      paths=(); flags=()
      set -f  # glob の展開を止める（--exclude=*_test.go をそのまま渡すため）
      for tok in $rest; do
        case "$tok" in
          --include=*|--exclude=*) flags+=("${tok//\'/}") ;;
          --*) set +f; err "$f:$n ssot-assert: 許可されていないフラグ $tok（--include= / --exclude= のみ）"; return ;;
          *) paths+=("$tok") ;;
        esac
      done
      set +f
      if [ ${#paths[@]} -eq 0 ]; then
        err "$f:$n ssot-assert: 照合先のパスが無い（$body）"; return
      fi
      for tok in "${paths[@]}"; do
        # 照合先が消えると absent-grep が「該当なし」で素通りするので存在を必須にする
        [ -e "$tok" ] || { err "$f:$n ssot-assert: 照合先が存在しない: $tok"; return; }
      done
      hits=$(grep -rnE ${flags[@]+"${flags[@]}"} -- "$pattern" "${paths[@]}" 2>/dev/null || true)
      if [ "$verb" = absent-grep ] && [ -n "$hits" ]; then
        err "$f:$n 時点依存の記述が実態と矛盾（absent-grep '$pattern' に該当あり）"
        printf '%s\n' "$hits" | head -3 | sed 's/^/          /' || true
      fi
      if [ "$verb" = present-grep ] && [ -z "$hits" ]; then
        err "$f:$n 時点依存の記述が実態と矛盾（present-grep '$pattern' に該当なし）"
      fi
      ;;
    path-exists|path-absent)
      paths=()
      set -f
      for tok in $rest; do paths+=("$tok"); done
      set +f
      if [ ${#paths[@]} -eq 0 ]; then
        err "$f:$n ssot-assert: パスが無い（$body）"; return
      fi
      for tok in "${paths[@]}"; do
        if [ "$verb" = path-exists ] && [ ! -e "$tok" ]; then
          err "$f:$n 時点依存の記述が実態と矛盾（$tok が存在しない）"
        fi
        if [ "$verb" = path-absent ] && [ -e "$tok" ]; then
          err "$f:$n 時点依存の記述が実態と矛盾（$tok が存在する）"
        fi
      done
      ;;
    manual)
      # 機械照合が原理的に不可能なものの許可リスト。理由を必須にする（§4）
      if ! printf '%s' "$rest" | grep -qE "^'[^']+'"; then
        err "$f:$n ssot-assert: manual は理由をシングルクォートで必須（$body）"
      fi
      ;;
    *)
      err "$f:$n ssot-assert: 未知の動詞 '$verb'（absent-grep / present-grep / path-exists / path-absent / manual）"
      ;;
  esac
}

# 3a. ssot-assert ディレクティブの評価
#
# 【コード表記の中の ssot-assert: は「記法の説明」であって指示ではない】
# 指示書は記法そのものを説明する場所でもあるため、コードブロック（``` / ~~~）と
# インラインコード（`...`）に囲まれた ssot-assert: は評価対象から外す。
# 除外しないと、記法を説明した瞬間に「書式が壊れている」と報告される。
#
# 捕捉していた誤検出の実例:
#   - .claude/commands/docs-ssot.md の WARN 振り分け表に
#     「| 原理的に照合できない | `ssot-assert: manual '<理由>'` で理由を残す |」という行があり、
#     HTML コメントではないため ERROR になっていた
#
# 【意図的に除外しないもの】
# HTML コメントとして正しく切り出せた場合は、コード表記の判定に入る前に評価する。
# 書き損じた実ディレクティブ（`<!--` を付け忘れた等）は従来どおり ERROR になる。
for f in $ALL_DOCS; do
  # コードブロック内の行は最初から候補にしない（フェンス行自体も対象外）
  hits=$(awk '
    /^[[:space:]]*(```|~~~)/ { fence = !fence; next }
    fence { next }
    /ssot-assert:/ { printf "%d:%s\n", NR, $0 }
  ' "$f")
  [ -z "$hits" ] && continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    n=${line%%:*}; body=${line#*:}
    directive=$(printf '%s' "$body" \
      | sed -n 's/.*<!--[[:space:]]*ssot-assert:[[:space:]]*\(.*\)-->.*/\1/p' \
      | sed 's/[[:space:]]*$//')
    if [ -n "$directive" ]; then
      eval_assert "$f" "$n" "$directive"
      continue
    fi
    # HTML コメントとして切り出せなかった。インラインコードを剥がしてなお
    # ssot-assert: が残るなら、記法の説明ではなく書き損じたディレクティブとみなす。
    if printf '%s' "$body" | sed 's/`[^`]*`//g' | grep -q 'ssot-assert:'; then
      err "$f:$n ssot-assert の書式が壊れている（<!-- ssot-assert: 動詞 引数... --> の形にする）"
    fi
  done <<EOF
$hits
EOF
done

# 3b. 「未整備」と書きつつ参照先が実在するケース（SKILL.md の実例）
#
# 対象語を「未実装・未導入」へ広げない: 「〜は未実装（docs/plans/x.md 参照）」のように
# 実在パスを併記するのが正しい書き方であるものまで誤検出するため。
# それらは 3c のマーカー WARN と ssot-assert で担保する。
while IFS= read -r f; do
  [ -z "$f" ] && continue
  hits=$(grep -nE '未整備' "$f" || true)
  [ -z "$hits" ] && continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    n=${line%%:*}; body=${line#*:}
    # 行内に実在するパスが書かれていれば「未整備」は嘘
    for p in $(printf '%s' "$body" | grep -oE '(docs|internal|scripts|configs)/[A-Za-z0-9_./-]*' || true); do
      if [ -e "${p%/}" ]; then
        err "$f:$n 「未整備」と書かれているが $p は存在する"
        break
      fi
    done
  done <<EOF
$hits
EOF
done <<EOF
$INSTRUCTION_DOCS
EOF

# 3c. 照合ルール（ssot-assert）が付いていない時点依存マーカーは WARN
#     ディレクティブは同じ行か直後の行に置く。
#     「未対応」は docs/testing/ の仕様表で意図的に使う語なので対象外。
MARKERS='現時点で|未導入|未整備|未実装'
while IFS= read -r f; do
  [ -z "$f" ] && continue
  offenders=$(awk -v m="$MARKERS" '
    { L[NR] = $0 }
    END {
      for (i = 1; i <= NR; i++) {
        if (L[i] !~ m) continue
        if (L[i] ~ /ssot-assert/) continue
        if (i < NR && L[i + 1] ~ /ssot-assert/) continue
        printf "%d\t%s\n", i, L[i]
      }
    }' "$f")
  [ -z "$offenders" ] && continue
  while IFS=$'\t' read -r n _; do
    [ -z "${n:-}" ] && continue
    warn "$f:$n 時点依存の記述に ssot-assert が無い → 照合ルールを添えるか manual で理由を残す"
  done <<EOF
$offenders
EOF
done <<EOF
$INSTRUCTION_DOCS
EOF
[ "$ERRORS" -eq "$before" ] && [ "$WARNS" -eq "$beforew" ] && echo "  OK"

# ---- 4. チェックリストの欠落写し ----------------------------------------------
#
# 「矛盾」より検出しにくい型: 正本の N 項目のうち M(<N) 項目だけを写したもの。
# 両方読んでも矛盾しないので人間は気づけない。
#
# 捕捉する実例:
#   - .claude/commands/feature-new.md が docs/testing/README.md §6 の
#     6項目チェックリストを4項目だけ写していた
#   - 同ファイルが「§6 のレビューゲート用チェックリスト（6項目）」と項目数を写しており、
#     正本が7項目になっても気づけない状態だった
#
# 判定は「項目の本文が一致するか」で行う。以前は「README 以外にチェックボックスがあれば
# WARN」という presence 判定だったため、正本と無関係なチェックリストまで
# 「§6 の写し」と報告していた。また正本の項目数を §6 に限定せずファイル全体から
# 数えていたため、README に別のチェックリストが増えた時点で表示が嘘になる形だった。
head_ "4. チェックリストの欠落写し"
before=$WARNS
beforee=$ERRORS
CHECKLIST_SSOT="docs/testing/README.md"

# 比較キー: チェックボックス記法・リンク記法・強調記号・空白を落とした本文
norm_item() {
  sed -e 's/^[[:space:]]*- \[[ x]\][[:space:]]*//' \
      -e 's/\[\([^]]*\)\]([^)]*)/\1/g' \
      -e 's/[`*_]//g' \
      -e 's/[[:space:]]//g' \
      -e 's/　//g'
}

ssot_items=$(awk '/^## 6\./ { s = 1; next } s && /^## [0-9]/ { exit } s' "$CHECKLIST_SSOT" \
  | { grep -E '^[[:space:]]*- \[[ x]\]' || true; })
ssot_n=$(printf '%s\n' "$ssot_items" | { grep -c . || true; })
ssot_range=$(awk '
  /^## 6\./ { s = NR; next }
  s && /^## [0-9]/ { print s "," NR - 1; found = 1; exit }
  END { if (!found && s) print s "," NR }' "$CHECKLIST_SSOT")

if [ "${ssot_n:-0}" -eq 0 ]; then
  err "$CHECKLIST_SSOT §6 のチェックリストが見つからない（正本の見出しが変わった可能性）"
fi

# 4a. 項目本文の写し
while IFS= read -r f; do
  [ -z "$f" ] && continue
  [ "$f" = "$CHECKLIST_SSOT" ] && continue
  keys=$(grep -E '^[[:space:]]*- \[[ x]\]' "$f" 2>/dev/null | norm_item || true)
  [ -z "$keys" ] && continue
  matched_n=0
  missing=""
  while IFS= read -r item; do
    [ -z "$item" ] && continue
    k=$(printf '%s\n' "$item" | norm_item)
    [ -z "$k" ] && continue
    if printf '%s\n' "$keys" | grep -qxF "$k"; then
      matched_n=$((matched_n + 1))
    else
      missing="${missing}${missing:+ / }$(printf '%s\n' "$item" | sed 's/^[[:space:]]*- \[[ x]\][[:space:]]*//')"
    fi
  done <<EOF
$ssot_items
EOF
  # 一致ゼロ = 正本と無関係なチェックリスト（タスク一覧等）なので対象外
  [ "$matched_n" -eq 0 ] && continue
  if [ -n "$missing" ]; then
    warn "$f が $CHECKLIST_SSOT §6 の間引かれた写し（${matched_n}/${ssot_n} 項目）→ リンクに置き換える"
    printf "          欠落: %s\n" "$missing"
  else
    warn "$f が $CHECKLIST_SSOT §6 を全項目写している（${ssot_n} 項目）→ リンクに置き換える"
  fi
done <<EOF
$INSTRUCTION_DOCS
EOF

# 4b. 項目数の写し（「§6 の…（6項目）」）が実数と合っているか
#     正本ファイル自身の §6 内の記述（「この6項目が正本」）も対象にする。
while IFS= read -r f; do
  [ -z "$f" ] && continue
  if [ "$f" = "$CHECKLIST_SSOT" ]; then
    from=${ssot_range%%,*}
    hits=$(sed -n "${ssot_range}p" "$f" | { grep -nE '[0-9]+[ 　]{0,3}項目' || true; } \
      | awk -F: -v off="$((from - 1))" '{ n = $1 + off; sub(/^[0-9]+:/, ""); print n ":" $0 }')
  else
    # 正本を指していると分かる行に限る（無関係な「3項目」を ERROR にしないため）
    hits=$({ grep -nE '[0-9]+[ 　]{0,3}項目' "$f" || true; } \
      | { grep -E '§[ ]?6|チェックリスト.*README\.md|README\.md.*チェックリスト' || true; })
  fi
  [ -z "$hits" ] && continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    n=${line%%:*}; body=${line#*:}
    num=$({ printf '%s' "$body" | grep -oE '[0-9]+[ 　]{0,3}項目' || true; } | head -1 | tr -dc '0-9')
    [ -z "$num" ] && continue
    if [ "$num" -ne "$ssot_n" ]; then
      err "$f:$n 項目数の写しが実数と不一致（記述 ${num} 項目 / $CHECKLIST_SSOT §6 は ${ssot_n} 項目）"
    fi
  done <<EOF
$hits
EOF
done <<EOF
$INSTRUCTION_DOCS
EOF
[ "$WARNS" -eq "$before" ] && [ "$ERRORS" -eq "$beforee" ] && echo "  OK"

# ---- 5. .claude/ 配下への正本宣言 ---------------------------------------------
#
# .claude/** は Claude Code 専用で、他の AI エージェント（ROADMAP の Gemini 等）からは
# 読めない。ここに規約の正本を置くと、非 Claude エージェントにとって AGENTS.md が
# 行き止まりのリンク集になる。
#
# 捕捉する実例:
#   - テスト設計原則が .claude/skills/go-testing-qa/references/ にあり、
#     AGENTS.md §3 がそこを「正本」として指していた
#
# 既知の検出漏れ: 「正本」という語を使わない言い回し（「SSoT は .claude/…」「原典は…」）は
# 抜ける。構造的な判定（「.claude/ への言及行は『専用』を含むこと」）も検討したが、
# AGENTS.md の「指示書の読み手と置き場」節に『専用』を含まない正当な言及が複数あり
# 誤検出が多いため採らなかった。語彙を増やす場合はここに追記する。
#
# 「正本」と `.claude/` の近接は 2b と同じ理由で `.{0,40}` で表す。以前の `[^。]*` は
# 「`.claude/…/SKILL.md` が正本である」を取りこぼしていた（多バイト文字の否定クラスと
# 選択肢の組み合わせで一致しなくなる。詳細は 2b のコメント）。
head_ "5. .claude/ 配下への正本宣言"
before=$ERRORS
DECL_CLAUDE='正本.{0,40}\.claude/|\.claude/.{0,40}が正本|正本は.{0,40}スキル'
while IFS= read -r f; do
  [ -z "$f" ] && continue
  case "$f" in .claude/*|CLAUDE.md) continue ;; esac
  hits=$(grep -nE "$DECL_CLAUDE" "$f" || true)
  [ -z "$hits" ] && continue
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    body=${line#*:}
    # 「.claude/ には正本を置かない」という規約文そのものは対象外
    case "$body" in *置かない*|*置く場所*) continue ;; esac
    err "$f:${line%%:*} .claude/ 配下を正本として宣言している → docs/ か AGENTS.md へ移す"
  done <<EOF
$hits
EOF
done <<EOF
$ALL_DOCS
EOF
[ "$ERRORS" -eq "$before" ] && echo "  OK"

# ---- 6. 正本表そのものの健全性 ------------------------------------------------
#
# AGENTS.md §3 の索引表が「どこを読むか」の唯一の入口。ここが壊れると
# 全エージェントが正本にたどり着けなくなる。
head_ "6. AGENTS.md §3 索引表"
before=$ERRORS
rows=$(ssot_index | wc -l | tr -d ' ')
if [ "$rows" -lt 3 ]; then
  err "AGENTS.md §3 の索引表が見つからない（または行数が異常: ${rows} 行）"
fi
targets=$(ssot_index | { grep -oE '\]\([^)]*\)|`[^`]*\.(md|sh)`' || true; } | sed 's/^](//;s/)$//;s/`//g')
while IFS= read -r t; do
  [ -z "$t" ] && continue
  p=${t%%#*}
  [ -e "$p" ] || err "AGENTS.md §3 索引表 -> $p （存在しない）"
done <<EOF
$targets
EOF
echo "  索引表 ${rows} 行 / 参照先 $(printf '%s\n' "$targets" | grep -c . || true) 件"
[ "$ERRORS" -eq "$before" ] && echo "  OK"

# ---- 結果 ---------------------------------------------------------------------
head_ "結果"
printf "  ERROR: %d / WARN: %d\n" "$ERRORS" "$WARNS"
if [ "$ERRORS" -gt 0 ]; then
  echo
  echo "  ERROR は実態と矛盾している記述。必ず直すこと。"
  echo "  WARN は将来腐る形をしている記述。放置してよいなら、なぜ照合できないかを"
  echo "  該当箇所に残すこと（docs/testing/principles/deterministic-verification.md §5）。"
  exit 1
fi
exit 0

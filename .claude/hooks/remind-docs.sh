#!/usr/bin/env bash
# Stop hook: ターン終了時、コード/インフラ変更があるのに資料が未更新ならリマインドする。
# AGENTS.md の Infrastructure Change Rules / アーキテクチャ規約と実装の乖離を防ぐ目的。
set -euo pipefail

input=$(cat)

# stop_hook_active が true の場合は、このリマインド由来の再停止。ループ防止のため何もしない。
if [ "$(printf '%s' "$input" | jq -r '.stop_hook_active // false')" = "true" ]; then
  exit 0
fi

cwd=$(printf '%s' "$input" | jq -r '.cwd // "."')
cd "$cwd" 2>/dev/null || exit 0

changed=$(git status --porcelain 2>/dev/null || true)
[ -z "$changed" ] && exit 0

# コード・インフラ・規約設定の変更
# （.go / .tf / ワークフロー yaml / Makefile / lint 設定 / テスト除外設定 / スクリプト）
# lint 設定とテスト除外設定は「規約の正本」なので、変更時は資料との整合確認が必要。
code_changed=$(printf '%s\n' "$changed" \
  | grep -E '\.(go|tf)$|\.github/workflows/.*\.ya?ml$|(^|/)Makefile$|make/.*\.mk$|(^|/)\.golangci(\.yml|-version)$|(^|/)\.testignore$|scripts/.*\.sh$' || true)

# 資料の変更（AGENTS.md / CLAUDE.md / ARCHITECTURE.md / ROADMAP.md）
docs_changed=$(printf '%s\n' "$changed" \
  | grep -E '(AGENTS|CLAUDE|ARCHITECTURE|ROADMAP)\.md' || true)

if [ -n "$code_changed" ] && [ -z "$docs_changed" ]; then
  jq -n '{
    decision: "block",
    reason: "コード/インフラの変更がありますが AGENTS.md / CLAUDE.md / terraform/ARCHITECTURE.md / ROADMAP.md が未更新です。変更内容がこれらの資料の記述（アーキテクチャ規約・Infrastructure Change Rules・構成図・ロードマップなど）と矛盾しないか確認し、必要なら更新してください。資料更新が不要な変更であれば、その旨を一言述べて終了してください。"
  }'
fi
exit 0

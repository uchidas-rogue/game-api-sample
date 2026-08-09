# 指示書（AI エージェント向けドキュメント）の整合チェック。
#
# コード側の決定論的検証（lint / gen check / coverage check）と対になるもの。
# 規約の正本が1箇所に保たれているか、記述が実態と乖離していないかを判定する。
# 判定の実体は scripts/docs-ssot-check.sh にあり、検査ごとに
# 「その検査が捕捉する既知の実例」がコメントで併記してある。

.PHONY: docs/check docs/ssot

## 指示書の SSoT 崩れ・実態との乖離を検査する（ERROR があれば失敗）
docs/check:
	@bash scripts/docs-ssot-check.sh

## AGENTS.md §3 の正本表（どの問いの正本がどこか）を表示する
docs/ssot:
	@bash scripts/docs-ssot-check.sh --list

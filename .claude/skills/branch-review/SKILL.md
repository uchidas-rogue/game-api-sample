---
name: branch-review
description: 2つのブランチ（既定は main と現在のブランチ）の差分を AGENTS.md / CLAUDE.md の規約に照らして多角的にレビューする手順書。観点ごとに並列サブエージェントへ委譲し、指摘は変異テスト（実装をわざと壊してテストが落ちるか確認）で裏取りしたうえで must/should/nit 形式にまとめ `.vscode/review/` へ出力する。「mainとの差分をレビューして」「〇〇ブランチをレビューして」「敵対的検証を含めてレビューして」といった依頼で使う。
---

# ブランチ間差分の多角的レビュー（敵対的検証つき）

このスキルは**レビュー専用**。コードは変更しない（§3 の変異テストによる一時改変を除き、検証後は必ず元に戻す）。

## 1. 対象と範囲を決める

- 比較元（BASE）と比較先（TARGET）を確認する。指示に無ければ BASE=`main`、TARGET=現在のブランチとする。どちらか曖昧なら先にユーザーへ確認する
- 差分取得は **`gh pr diff` を使わず**、必ず `make review/*` 系を使う（生成物除外のため。理由は `make/review.mk` 冒頭コメント参照。生成物混入で読み取り窓が埋まりレビューが0件で終わった実例がある）
  - 全体像を掴む: `REVIEW_BASE=<BASE> make review/diff/stat`
  - ファイル一覧: `REVIEW_BASE=<BASE> make review/files`
  - 領域を絞った差分: `REVIEW_BASE=<BASE> REVIEW_PATHS='<path...>' make review/diff`

## 2. 観点ごとに分割する

ファイル一覧を見て、次の4観点を基本に、**実際に変更があった領域だけ**採用する。小さい差分（目安: 変更ファイル10未満かつ+300行未満）なら分割せず自分1人でレビューしてよい。

| 観点 | 対象パス例 | 主な参照条文 |
| --- | --- | --- |
| ① driver実装 | `internal/driver/**`, `internal/infrastructure/server/**`, `cmd/**` | AGENTS.md §1・§2（層依存・ミドルウェア/インターセプタ登録順・graceful shutdown・logger DI・context伝播・interface assertion） |
| ② usecase/infrastructure実装 | `internal/usecase/**`, `internal/infrastructure/**`（server以外）, `internal/domain/**`, `configs/**` | AGENTS.md §1・§2（Clock DI・エラー変換・設定管理）, §4（DB/トランザクション） |
| ③ テスト・カバレッジ | 変更された全 `*_test.go`, `docs/testing/**`, `**/mock/**` | AGENTS.md §3 |
| ④ ドキュメント・ビルド/CI | `docs/**`（testing以外含む）, `Makefile`, `make/**`, `.github/**`, `.golangci.yml`, `scripts/**`, `proto/**`, `go.mod`/`go.sum`, `README.md`, `ROADMAP.md`, `clients/**`, `web/**`, `loadtest/**` | AGENTS.md §5・「指示書の読み手と置き場」 |

観点は固定割当ではなく実際の変更ファイル分布に合わせて増減する（例: driverの変更が無ければ①は不要。逆に巨大な差分なら①をさらに分割する）。

## 3. 各観点を Agent tool で並列委譲する

`subagent_type: general-purpose` で、観点の数だけ**同一メッセージ内で並列に** Agent tool を呼ぶ（Agent はフレッシュな状態で起動されるため、以下を毎回プロンプトに含める）。

- リポジトリパス、BASE/TARGET ブランチ名
- 「まず `AGENTS.md` と `CLAUDE.md` を通読してから始めること」（規約はフレッシュなエージェントには見えていない）
- 差分取得コマンド（`REVIEW_PATHS` を担当領域に絞ったもの）。**ハンクだけでなく該当ファイルは全文読むこと**（登録順やレイヤ依存はハンク単位では判断できない）
- 担当領域のレビュー観点（AGENTS.md の該当節への具体的な参照）
- **敵対的検証を必須にする**: 「規約違反で実害が出る」「このテストは不備を検知できる」と主張する指摘は、一時的に実装を壊して該当テストが実際に red になるかを確認してから報告する。最低2〜3箇所は実施する
  - 検証後は必ず元に戻し、`git diff` で自分が触ったファイルに差分が残っていないか確認してから終了する。他の未コミット差分には触れない
  - 既に lint/archcheck/ruleguard/doccheck 等で機械的に強制されている規約は、実際に効いているかだけ確認し、二重に指摘しない
- 出力形式: must/should/nit の3段階。各指摘に ファイルパス:行番号・規約根拠（引用）・具体的な失敗シナリオ・検証方法と結果を含めること。水増しせず確信度の高いものを優先する

## 4. 集約前にワークツリーの健全性を確認する

全エージェントの完了通知を受け取るたびに `git status --porcelain -uall` を確認する。汚れていたら:

- `git diff -- <file>` で中身を確認する（変異テストの戻し忘れである可能性が高い）
- 意図した変更でなければ内容を一言説明したうえで戻す。並行して動く別エージェントが直後に自力で戻すこともあるため、即断即決で `checkout` する前に中身を読んで判断する
- 全エージェント完了後、最終的に作業ツリーがクリーンであることを確認してから次に進む

## 5. 集約してレポートを書く

- 各エージェントの指摘を重複排除しつつ must → should → nit の順にまとめる
- 冒頭に: 対象範囲・差分規模・実行した機械チェック（該当するもの: `make lint` / `make test` / `make test/race` / `make test/cover/check` / `make gen/check` / `make docs/check` / `make proto/gen/check` / `make site/check`）の結果・敵対的検証で実施した変異テストの一覧（壊した箇所・手法・結果）を書く
- 出力先: `.vscode/review/<target>_vs_<base>.md`（`.vscode/review/` が無ければ作成する）
- チャットへの返答は要点のみ（must件数・should/nitの概要）にとどめ、詳細はファイルを見てもらう

## 6. 注意

- このスキル自体はコードを変更しない。作業完了後に Stop hook がドキュメント未更新を指摘してきた場合、レビューのみで実装変更が無ければ「変更なしのため資料更新不要」と回答してよい
- 大きな差分では並列サブエージェントが多くのトークン・時間を消費する。観点を絞れないか一度検討してから起動する

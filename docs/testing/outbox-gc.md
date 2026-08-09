# Outbox GC バッチのテスト設計

対象: [internal/driver/batch/outbox_gc.go](../../internal/driver/batch/outbox_gc.go)
テスト: [internal/driver/batch/outbox_gc_test.go](../../internal/driver/batch/outbox_gc_test.go)

運用ルールは [README.md](README.md)。worker 側の設計は [outbox-worker.md](outbox-worker.md)。

処理済みの `outbox_events` を保持期間経過後に削除する常設バッチ。
worker は `processed_at` を立てるだけで行を消さないため、これが無いとテーブルが単調増加する。
負荷試験の実測（約 400 events/sec）では 1 日あたり約 3,400 万行が積み上がる。

## フローチャート

```mermaid
flowchart TD
    A[Run 開始] --> B[[DoInTx 境界に入る<br/>1チャンク = 1トランザクション]]
    B -- 境界の確立/確定に失敗 --> E1((outbox gc tx エラー))
    B -- fn 実行 --> C[repo.DeleteProcessedBefore<br/>retention より古い処理済みを最大 chunkSize 件]

    C -- err --> E2((delete エラー))
    C -- ok --> D[[COMMIT]]
    D --> F{削除件数 == chunkSize か}

    F -- No --> Z([対象が尽きた<br/>総削除件数をログに出して終了])
    F -- Yes --> G{ctx が生きているか}
    G -- No --> E3((ctx のエラー))
    G -- Yes --> B
```

**設計上の要点**（テストで守る不変条件）:

- **1 チャンク = 1 トランザクション**にする。1 文で全削除すると undo ログとロック保持が
  肥大し、同じテーブルへの INSERT（リクエスト経路の outbox 記録）を阻害する。
  チャンクサイズは driver 層の定数（環境ごとに変える必要が出たら `configs` へ移す）
- 基準時刻は **SQL 側の `NOW(6)`** で取る。アプリ側で現在時刻を取得すると AGENTS.md §2 の
  Clock DI 規約に抵触するため、repository には「時刻」ではなく**保持期間**を渡す
  （規約の実態照合は AGENTS.md §2 の `ssot-assert` が担当。ここでは重複させない）
- **未処理（`processed_at IS NULL`）は削除しない**。恒久失敗イベントの始末は
  max retry / DLQ の責務（[outbox-worker.md](outbox-worker.md) §4）であり、
  GC が消すとイベントが黙って失われる
- 終了条件は `削除件数 < chunkSize`。0 件でなくても、その回で対象が尽きたことを意味する
- 途中で失敗したら**その時点で打ち切ってエラーを返す**。GC は次回の実行で続きを消せるため、
  [ranking-sync-batch.md](ranking-sync-batch.md) のように「残りを試行してから報告」する必要がない
  （あちらは復旧が目的なので、届く分を届けることに意味がある）

## テスト仕様表

パスが短い順。**表の1行 = テストコードの1ケース。**

| # | 条件 | 図のパス | 期待結果 | 検証すべき呼び出し |
| --- | --- | --- | --- | --- |
| 1 | `DoInTx` 自体が失敗 | `A→B→E1` | エラーを返す | 削除が呼ばれない |
| 2 | 削除が失敗 | `A→B→C→E2` | エラーを返す | 2 チャンク目が呼ばれない |
| 3 | 対象なし（0 件） | `…→C→D→F→Z` | 正常終了 | 削除が **1 回だけ** 呼ばれる |
| 4 | 端数で終わる（0 < 件数 < chunkSize） | `…→F→Z` | 正常終了 | 同上 |
| 5 | チャンク満杯 → 次で端数 | `…→F→G→B→…→F→Z` | 正常終了 | 削除が **2 回** 呼ばれる |
| 6 | チャンク満杯 → ctx キャンセル | `…→F→G→E3` | `context.Canceled` を返す | 次のチャンクへ**進まない** |

**ケース 3 と 4 を統合しない理由**: パスは同一だが、終了条件を
`削除件数 > 0 で継続` のように書き間違えると、ケース 4（端数）だけが無限ループになり
ケース 3（0 件）では落ちない。http-gacha.md のケース 1・2 と同じ「片方だけが検出できる
実装ミスがある」に該当する。

**ケース 6 について**: `ctx` が死んでいれば実際には次の `DoInTx`（BeginTx）が失敗するため、
この判定は防御的なもの。ただし [worker.go](../../internal/driver/worker/outbox/worker.go) の
ドレインループが同じ形の歯止めを持っており、そちらと構造を揃えている。

## パスの網羅状況

終端ノードは `E1`・`E2`・`E3`・`Z` の **4 個**。上表はすべてを最低 1 件ずつ通っている。
`Z` は 3 通りの入り方（0 件・端数・複数チャンク後）をケース 3・4・5 で個別に通している。

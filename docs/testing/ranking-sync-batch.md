# ランキング同期バッチのテスト設計

対象: [internal/driver/batch/ranking_sync.go](../../internal/driver/batch/ranking_sync.go)
テスト: [internal/driver/batch/ranking_sync_test.go](../../internal/driver/batch/ranking_sync_test.go)

運用ルールは [README.md](README.md)。

MySQL のスナップショットを Redis へ全件反映する定期バッチ。
outbox の pending イベントとの二重反映を避けるため、**スナップショット境界（outbox の最大 ID）を
先に取得し、その境界までのイベントを processed にマークする**設計になっている。

## フローチャート

```mermaid
flowchart TD
    A[SyncAll 開始] --> B[[DoInTx 境界に入る]]
    B -- 境界の確立/確定に失敗 --> E1((ranking sync tx エラー))
    B -- fn 実行 --> C[outboxRepo.GetMaxID<br/>スナップショット境界を先に確定]

    C -- err --> E2((get max outbox id エラー))
    C -- ok --> D[repo.ListAllGuildScores]
    D -- err --> E3((list all guild scores エラー))
    D -- ok --> E[repo.ListAllUserPoints]
    E -- err --> E4((list all user points エラー))
    E -- ok --> F[outboxRepo.MarkProcessedUpTo<br/>maxID まで・event_type を明示して絞る]

    F -- err --> E5((mark outbox processed エラー))
    F -- ok --> G[[COMMIT]]

    G --> H[rankingStore.SetGuildScore / SetUserPoints<br/>全件ループ]
    H -- 個別 SET が失敗 --> I[ERROR ログのみ<br/>ループを継続]
    I --> H
    H -- 全件処理 --> Z([nil を返す])
```

**設計上の要点**（テストで守る不変条件）:

- `GetMaxID` を**最初に**呼ぶ。境界を先に固定しないと、処理中に積まれたイベントを
  誤って processed にしてしまう
- `MarkProcessedUpTo` は `event_type` を明示して絞る。将来ドメインが増えたときに
  ranking 以外のイベントを巻き添えにしないため
- Redis 反映は **COMMIT 後**。個別 SET の失敗はログのみで**処理を継続し、エラーを返さない**
  （次回の同期で回復する）

## テスト仕様表

| # | 条件 | 図のパス | 期待結果 | 検証すべき呼び出し |
| --- | --- | --- | --- | --- |
| 1 | `DoInTx` 自体が失敗 | `A→B→E1` | エラーを返す | 内部処理も Redis SET も呼ばれない |
| 2 | `GetMaxID` が失敗 | `A→B→C→E2` | エラーを返す | Redis SET が呼ばれない |
| 3 | `ListAllGuildScores` が失敗 | `…→C→D→E3` | エラーを返す | 同上 |
| 4 | `ListAllUserPoints` が失敗 | `…→D→E→E4` | エラーを返す | 同上 |
| 5 | `MarkProcessedUpTo` が失敗 | `…→E→F→E5` | エラーを返す | 同上 |
| 6 | 正常系: 空テーブル（`maxID=0`） | `…→F→G→Z` | 正常終了 | `MarkProcessedUpTo` に `maxID=0` が渡る（0 件 UPDATE で安全） |
| 7 | 正常系: スナップショットが反映される | `…→G→H→Z` | 正常終了 | `MarkProcessedUpTo` に **`EventTypeRankingScoreAdded` が渡る**／`SetGuildScore` / `SetUserPoints` が全件ぶん呼ばれる |
| 8 | Redis SET の一部が失敗 | `…→H→I→H→Z` | **エラーを返さない** | 1件目が失敗しても**残り全件の SET が呼ばれる** |

**エラーの伝搬について**: 1〜5 はいずれも `errors.Is` で原因のエラーを辿れること
（ラップが原因をすり替えないこと）をランナーで一律に検証している。

## 本設計文書の作成で見つかった問題

既存テストは 8 ケースで、上表のパスをすべて通っていた（**パスの欠落は無し**）。

一方でテーブル駆動の形に問題があった。

| 種別 | 内容 | 対応 |
| --- | --- | --- |
| **テーブル駆動の形** | 各ケースが `setup func(t, ctrl) *batch.RankingSyncer` を持ち、モックの組み立て手順をテーブル側に書いていた | テーブルをデータのみにし、組み立てはランナーへ集約 |
| **検証の欠落** | エラーケースで `require.Error` しか見ておらず、**どのエラーが返るか**を確認していなかった（`errors.Is` での検証は別テスト関数に1件だけ切り出されていた） | ランナーで全エラーケースに `assert.ErrorIs` を適用し、切り出されていた関数を統合 |
| **デッドコード** | `expectDoInTxNotCalled` というコメントだけのヘルパー宣言（「現状未使用」と明記されていた）が残っていた | 削除 |

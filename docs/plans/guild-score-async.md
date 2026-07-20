# ギルドスコア更新の非同期化

> ステータス: **実装済み（2026-07-20）**
> 実施契機: 負荷試験（k6 `make load/points`）で同一ギルド集中加算のレイテンシ悪化（p99 14s 台・スループット頭打ち）を観測したため実施。
> 実装時に本プランから乖離した点は末尾「実装メモ（プランからの差分）」を参照。

## Context

`AddUserPoints` は API リクエスト1回ごとに同一トランザクション内で MySQL の `user_points` と `guild_scores` 両方を更新している。`guild_scores` は1ギルド=1行の構造のため、同一ギルド内で複数ユーザーが同時に加算するとX ロック競合でレスポンスが直列化・遅延する。ユーザー数の多いギルドでGvGイベント等の同時加算が発生する場面で顕在化する想定。

本プランでは API tx からは個人系の更新のみ残し、ギルド側の MySQL 更新（`guild_scores` 加算と `guild_score_histories` 挿入）を outbox-worker に移して非同期化する。これにより API レスポンスから `guild_scores` 行ロックの保持時間を完全に除去し、同ギルド内の同時加算がギルドスコア行ロックに影響されなくなる。

トレードオフ: MySQL の `guild_scores` は数秒〜数十秒の遅延を伴って反映される。ただしランキング読み取りは Redis 経由のため UX への影響はなし。Redis ZSet が揮発した際の再構築は既存の batch（[internal/driver/batch/ranking_sync.go](../../internal/driver/batch/ranking_sync.go)）が担う。

## 設計判断（決定済み）

### 1. API レスポンスの変更
`UserPointAddResult` から `GuildPreviousTotal` / `GuildNewTotal` を削除する。`GuildID` は残す。
理由: ギルドスコア更新が非同期になるため、API 同期レスポンスでは正確な値を返せない。

### 2. worker のトランザクション境界
現状のバッチ単位 tx をイベント単位 tx に変更する。
理由: A案では worker 内で MySQL `guild_scores` 更新が増えるため、partial failure 時に同バッチ内の他イベント処理結果と一緒に MySQL 変更がコミットされてしまうと、再試行時の二重加算リスクがある。イベント単位 tx にすることで失敗時に MySQL 変更がロールバックされる。

### 3. handleEvent 内の処理順序
1. `rankingRepo.IncrementGuildScore`（MySQL 加算）
2. `rankingRepo.InsertGuildScoreHistory`（MySQL 履歴）
3. `rankingStore.IncrementUserPoints`（Redis）
4. `rankingStore.IncrementGuildScore`（Redis）

意図: Redis を最後に置くことで、Redis 失敗時に MySQL もロールバックされる。

## 変更対象ファイル（実装時のチェックリスト）

### ドメイン
- [ ] [internal/domain/ranking/entity.go](../../internal/domain/ranking/entity.go)
  `UserPointAddResult` から `GuildPreviousTotal`, `GuildNewTotal` を削除（`GuildID` は残す）

### usecase
- [ ] [internal/usecase/ranking/usecase.go](../../internal/usecase/ranking/usecase.go) `AddUserPoints`
  - 削除: `GetGuildScore`（previousTotal 算出のためだけに使用、不要になる）
  - 削除: `InsertGuildScoreHistory` 呼び出し
  - 削除: `IncrementGuildScore` 呼び出し
  - 削除: 結果構造体への `GuildPreviousTotal` / `GuildNewTotal` 設定
  - 残す: `GetUser`, `GetUserGuildID`, `GetUserPoints`, `InsertUserPointHistory`, `IncrementUserPoints`, `outboxRepo.InsertEvent`, `outboxNotifier.Notify`
  - ※ `GetUserGuildID` は outbox payload に guild_id を載せるため必須

- [ ] [internal/usecase/ranking/usecase_test.go](../../internal/usecase/ranking/usecase_test.go) AddUserPoints の各ケース
  - `GetGuildScore`, `InsertGuildScoreHistory`, `IncrementGuildScore` の EXPECT を削除
  - 結果検証から `GuildPreviousTotal` / `GuildNewTotal` のアサート削除
  - 「`ErrScoreNotFound` を正常系として扱う」ケースは不要なので削除

### handler
- [ ] [internal/driver/http/ranking/handler.go](../../internal/driver/http/ranking/handler.go)
  `AddUserPoints` のレスポンス DTO から guild の previous/new total フィールドを削除
- [ ] `internal/driver/http/ranking/handler_test.go`
  該当アサート削除

### worker
- [ ] [internal/driver/worker/outbox/worker.go](../../internal/driver/worker/outbox/worker.go)
  - `Worker` 構造体と `Config` に `rankingRepo rankingusecase.Repository` を追加
  - `runOnce` を改修: ListPending を1 tx で取得 → 即コミット → イベントごとに `DoInTx` でラップした `handleEvent` + `MarkProcessed`/`IncrementRetry` を実行する構造に変更
  - `handleEvent` シグネチャは既に `tx shared.Tx` を受け取る形（現状未使用、[worker.go:143](../../internal/driver/worker/outbox/worker.go#L143)）。tx を実際に使うように変更
  - `EventTypeRankingScoreAdded` ハンドラに上記「処理順序」の通り MySQL 更新を追加

- [ ] `internal/driver/worker/outbox/worker_test.go`
  - 既存ケースを「rankingRepo の `IncrementGuildScore`/`InsertGuildScoreHistory` も呼ばれる」前提に修正
  - 追加ケース: MySQL 更新失敗 → Redis 呼ばれず、tx ロールバック、IncrementRetry が別 tx で記録される
  - 追加ケース: イベント単位 tx 化により1イベント失敗が他イベント処理に影響しない
  - mock は既存の [internal/usecase/ranking/mock/mock_repository.go](../../internal/usecase/ranking/mock/mock_repository.go) を再利用

### DI
- [ ] [cmd/outbox-worker/main.go](../../cmd/outbox-worker/main.go) (47-60行付近)
  `repository.NewRankingRepository(db)` を追加し、`workeroutbox.Config` の新 `RankingRepo` フィールドに渡す

## 再利用する既存リソース

- **`outboxdomain.RankingScoreAddedPayload`**（[internal/domain/outbox/event.go](../../internal/domain/outbox/event.go)）: 既に `UserID`, `GuildID`, `Points` を含む。スキーマ変更不要
- **`rankingusecase.Repository.IncrementGuildScore` / `InsertGuildScoreHistory`**（[internal/usecase/ranking/repository.go](../../internal/usecase/ranking/repository.go)）: 既に `tx shared.Tx` を受け取るシグネチャ。worker からそのまま呼べる
- **`shared.Transactor.DoInTx`**: worker は既に依存しており、イベント単位 tx もこれで実現可能

## 影響範囲外（今回触らない）

- `outbox_events` スキーマ
- マイグレーション
- batch（ranking_sync）: 揮発時再構築の振る舞いは変わらない
- Redis 側の処理（IncrementUserPoints / IncrementGuildScore のシグネチャ）

## 既知のリスク（プラン外）

- **Redis 二重 incr**: tx ロールバック時 Redis は元に戻らない。これは現状コードでも存在する問題で、本プランで悪化はしないが解消もしない。改善するなら別タスクで「outbox イベントに idempotency key を持たせ Redis 側でセット管理」等が必要
- **MySQL `guild_scores` の遅延**: 数秒オーダー。ランキング表示は Redis 経由のため影響なし
- **DLQ・max retry 上限**: 既存の未対応課題。本プランで永続的失敗イベントが増える可能性は変わらない

## 検証手順

1. **ユニットテスト**: `make test`
   - `internal/usecase/ranking/usecase_test.go` AddUserPoints: ギルド系 mock EXPECT が削除されてもパスすること
   - `internal/driver/worker/outbox/worker_test.go`: rankingRepo mock 経由で MySQL 更新が呼ばれることを検証
   - `internal/driver/http/ranking/handler_test.go`: レスポンス DTO 変更後もパスすること
2. **lint**: `make lint`
3. **ローカル E2E 確認**:
   - `docker compose up -d`（MySQL + Redis）+ `make db/migrate/up`
   - `make run`（API）と `go run ./cmd/outbox-worker`（worker）を別ターミナルで起動
   - `curl` で `AddUserPoints` を1回叩き、レスポンスに guild の previous/new total が無いことを確認
   - 数秒後に MySQL `SELECT * FROM guild_scores WHERE guild_id = ?` で値が反映されていることを確認
   - MySQL `SELECT * FROM guild_score_histories WHERE guild_id = ?` で履歴が記録されていることを確認
   - Redis `ZSCORE guild_ranking <guild_id>` で ZSet も加算されていることを確認
4. **負荷確認（本プランの主目的）**: k6 で同一ギルド内 100 並行加算を投げ、レイテンシ p95/p99 が改善すること、および同時にギルドスコア行のロック競合に起因するエラーが発生しないことを確認

## 実装メモ（プランからの差分）

実装時、プラン策定時に見落としていた整合性リスクへの対応として以下を追加・変更した。

### 1. RankingSyncer（sync-rankings batch）を「再構築専用」に変更（プランは「触らない」としていた）
- **問題**: 本非同期化により不変条件が反転する。従来は「outbox イベントが pending ⟹ guild_scores は既に同期加算済み」だったが、非同期化後は「pending ⟹ guild_scores はまだ未加算（worker が後で適用）」になる。この状態で RankingSyncer の `MarkProcessedUpTo` が pending イベントを未加算のまま processed 化すると、worker がスキップして**そのギルド加算が guild_scores にも Redis にも永久に反映されず消失**する（data loss）。
- **対応**: RankingSyncer から `GetMaxID` / `MarkProcessedUpTo` / outbox 依存を除去し、`guild_scores` / `user_points` を読んで Redis へ SET するだけの「揃え直し専用ツール」にした（[internal/driver/batch/ranking_sync.go](../../internal/driver/batch/ranking_sync.go)）。worker がイベント処理の唯一の所有者となり整合する。
- **トレードオフ**: worker 稼働中に走らせるとスナップショット取得後に反映された数件が SET で上書きされ Redis 側で一時欠落しうる（MySQL は常に正しく次回再構築で自己修復）。原則、揮発復旧など書き込みが静穏な状況で実行する。

### 2. worker のイベント単位 tx を「候補取得 → id 指定 claim」方式で実装（複数 worker 安全性 + head-of-line blocking 回避）
- プラン記載の「ListPending で一括取得 → 即コミット → イベントごとに DoInTx」は、fetch の `FOR UPDATE SKIP LOCKED` ロックがコミットで解放されるため、複数 worker 構成（`worker_desired_count` は可変）で同一イベントを二重処理しうる。
- **対応**: worker は `ListPending` で候補 id 群を取得し、各候補を **id 指定** で claim する。sqlc クエリ `ClaimPendingOutboxEventByID`（`WHERE id=? AND processed_at IS NULL FOR UPDATE SKIP LOCKED`）を追加し、outbox Repository に `ClaimByID` を実装。worker は「候補ごとに DoInTx { ClaimByID → handleEvent → MarkProcessed }」を回し、handleEvent 失敗時は別 tx で IncrementRetry を記録する。これにより複数 worker 安全性（SKIP LOCKED の設計意図）・MySQL 副作用の exactly-once・イベント単位のロールバック分離を保つ。
- **id 指定 claim にした理由（当初 `ClaimNext`＝最小 id 固定取得で実装 → 修正）**: 最小 id を毎回 claim する方式だと、先頭イベントが恒久失敗（未知 event_type・壊れた payload 等の poison）した場合、毎ティック同じイベントを掴み続け、**後続イベントが永久に処理されない head-of-line blocking** が発生した。既知課題「Outbox DLQ・max retry 上限の未対応」を悪化させるため、候補を id 指定で処理する方式に修正し、先頭が失敗しても後続が進むようにした（`internal/driver/worker/outbox/worker_test.go` に回避を検証するケースあり）。なお poison を恒久的に打ち切る max-retry/DLQ は引き続き別課題。
- これに伴い不要となった outbox の `GetMaxID` / `MarkProcessedUpTo`（sqlc クエリ・Repository メソッド）は削除した。

### 3. 検証結果
- `make test` / `make lint` / `make test/race` 通過。カバレッジ: usecase/ranking 100%・driver/worker/outbox 94.7%・driver/batch 100%・infra/repository 86.0%・driver/http/ranking 97.4%。
- 負荷確認（`make load/points`）は本コミット後に再測定予定。

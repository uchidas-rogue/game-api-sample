# ギルドスコア更新の非同期化

> ステータス: **未着手 / 後フェーズで実施**
> 想定実施タイミング: 負荷試験で同一ギルド内同時加算のレイテンシ悪化が観測されたタイミング

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

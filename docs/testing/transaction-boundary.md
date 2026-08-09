# トランザクション境界の契約テスト

対象: [internal/infrastructure/mysql/transactor.go](../../internal/infrastructure/mysql/transactor.go)
テスト: [internal/infrastructure/mysql/transactor_test.go](../../internal/infrastructure/mysql/transactor_test.go)

`Transactor` は「複数の操作をひとまとまりの成功/失敗単位でくくる」仕組みそのもの。
分岐フローではなく**契約**を持つ対象なので、フロー図ではなくシナリオ表で管理する。

**この契約は境界を提供する `Transactor` 自身のテストでのみ検証する。**
境界を使う側（`usecase` 層）のテストでは `Transactor` をモックしてよく、
契約の検証をそちらに重複させない。

## 必須シナリオ

| # | シナリオ | 期待される契約 | 対応テスト |
| --- | --- | --- | --- |
| 1 | 境界内の処理（`fn`）が失敗する | ROLLBACK を実行してから、**元の失敗原因をそのまま再送出する**。ROLLBACK の実行が失敗原因をすり替えない | `異常系_fnがerrorを返すとROLLBACKされる` |
| 2 | 取り消し処理（ROLLBACK）自体が失敗する | ROLLBACK の失敗を握り潰さずログに残し、**それでも元の失敗原因を再送出する** | `異常系_ROLLBACK失敗でも元のエラーを返しログに残す`<br>`異常系_panic時のROLLBACK失敗でも再panicしログに残す` |
| 3 | 確定処理（COMMIT）自体が失敗する | COMMIT の失敗をそのまま呼び出し元に伝搬する | `異常系_Commit失敗はラップされる` |
| 4 | 境界内の処理が値なしで成功する | 値なしを正しく確定し、そのまま返す（異常系と誤認しない） | `正常系_fnがnilを返すとCOMMITされる` |

## 本リポジトリ固有の追加シナリオ

| # | シナリオ | 期待される契約 | 対応テスト |
| --- | --- | --- | --- |
| 5 | 境界の確立（BEGIN）に失敗する | `fn` を実行せずエラーを返す | `異常系_BeginTx失敗はラップされる` |
| 6 | 境界内で panic する | ROLLBACK してから**元の panic を再送出する** | `異常系_panic時にROLLBACKして再panic` |
| 7 | ROLLBACK が `sql.ErrTxDone` を返す | 既にコミット済みの正常な状態なのでログに出さない。元の失敗原因は返す | `ROLLBACK_ErrTxDoneはloggerに出力されない` |
| 8 | `fn` に渡される `Tx` の実体 | `*SQLTx` が渡り、`IsTx()` / `Raw()` が呼べる | `正常系_fnにSQLTxが渡される`<br>`SQLTx_Raw_内部のsqlTxを返す` |
| 9 | 分離レベルを明示する | オプション無し／`IsolationDefault` は driver 既定（`*sql.TxOptions` が `nil`）、`IsolationReadCommitted` は `sql.LevelReadCommitted` で BEGIN する | `分離レベル`（3 サブテスト） |

**シナリオ 9 の検証方法について**: go-sqlmock は `BeginTx` に渡る `*sql.TxOptions` を
検証できないため、疎通だけを見ると「常に nil を返す」実装でも通ってしまう。
変換関数 `toSQLTxOptions` を `export_test.go` 経由で直接呼んで戻り値を検証し、
併せてオプション付きでも BEGIN/COMMIT が通常どおり行われることを確認している。

## 分離レベルの使い分け

既定は DB のデフォルト（MySQL では REPEATABLE READ）。変更が必要な箇所だけ
トランザクション開始時に `shared.WithIsolation` で明示する（AGENTS.md §4）。

| 利用箇所 | 分離レベル | 理由 |
| --- | --- | --- |
| outbox worker の全 tx | READ COMMITTED | REPEATABLE READ だと `ListPending` の `SELECT ... FOR UPDATE` がギャップロックを取り、API 側の outbox INSERT を `INSERT_INTENTION` 待ちでブロックする（[outbox-worker.md](outbox-worker.md)）。worker は同一 tx 内の読み取り一貫性に依存しない |
| `RankingSyncer` | 既定（REPEATABLE READ） | `guild_scores` と `user_points` のスナップショットが互いに整合している必要がある（[ranking-sync-batch.md](ranking-sync-batch.md)） |
| API リクエスト経路 | 既定 | 明示が必要な事情が無い |

## 境界の外でやること

- 境界内で行う処理は「取り消し不可能な処理（ロック取得が必要な処理等）」に限定する
- ロックを伴わない事前検証・値解決は境界の外で完了させる。境界内に持ち込むとロック保持時間が延び、
  並行処理時の待ち合わせが増える
  - 実例: `gacha.Multi` は `pullCount` の妥当性検証を `DoInTx` の**外**で行っている
    （[docs/testing/gacha.md](gacha.md) のケース 1・2 が「`DoInTx` が呼ばれない」ことを検証している）
- 複数の永続化対象に同時にロックを取る場合は取得順序を決定的にする
  - 実例: `gacha.Multi` の `users → user_items（item_id 昇順）`
    （[docs/testing/gacha.md](gacha.md) のケース 16）

## 本設計文書の作成で見つかった問題

シナリオ 2（**取り消し処理自体が失敗するケース**）に対応するテストが存在しなかった。

`sql.ErrTxDone` が返るケース（シナリオ 7）のテストはあったが、
それ以外のエラーで ROLLBACK が失敗したときにログ出力の分岐へ入る経路が
通常経路・panic 経路ともに未検証だった。

「ROLLBACK の失敗が元の失敗原因をすり替えないこと」は握り潰しバグに直結するため、
`assert.NotErrorIs` で明示的に検証するケースを追加した。

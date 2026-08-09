# ランキング機能のテスト設計

対象: [internal/usecase/ranking/usecase.go](../../internal/usecase/ranking/usecase.go)
テスト: [internal/usecase/ranking/usecase_test.go](../../internal/usecase/ranking/usecase_test.go)

運用ルールは [README.md](README.md)。

`internal/domain/ranking` は図を作らない。`IsValidScore` / `NormalizeLimit` / `NormalizeOffset` の
境界値の正本は [constants_test.go](../../internal/domain/ranking/constants_test.go) のケース配列。

> **本ファイルの前提**: 境界値は判定ロジックを実装している層（domain）に集約する。
> usecase 層は「下位層の判定結果を受けた分岐」だけを検証し、domain が既に担保した
> 境界値バリエーションを重ねてテストしない。

---

## 1. 読み取り系: `GetGuildRankings` / `GetUserRankings`

2 メソッドは構造が同一（ギルド版とユーザー版）。**同じ図・同じケース構成を持つ**。
片方にだけケースがある状態にしない（対称性）。

```mermaid
flowchart TD
    A[開始] --> B[NormalizeLimit / NormalizeOffset<br/>domain で正規化]
    B --> C[rankingStore.GetXxxRankings<br/>正規化後の offset/limit を渡す]
    C -- err --> E1((get rankings エラー))
    C -- ok --> D{entries が空か}
    D -- Yes --> F[rankingStore.GetXxxTotalCount]
    D -- No --> G[repo.ListXxxByIDs<br/>名前解決]
    G -- err --> E2((list エラー))
    G -- ok --> H{entry の ID が<br/>名前マップにあるか}
    H -- Yes --> H2[Name をセット]
    H -- No --> H3[Name は空のまま<br/>エントリは除外しない]
    H2 --> F
    H3 --> F
    F -- err --> E3((total count エラー))
    F -- ok --> Z([RankingsResult を返す])
```

**`H` の No 側について**: ランキングの正本は Redis の ZSet、名前の正本は MySQL であり、
両者はずれうる（ユーザー/ギルド削除、ZSet 揮発後の再構築中など）。
このとき**エントリを黙って落とすと順位と件数が食い違う**ため、Name を空のまま返す設計にしている。
図に現れない `if _, ok := ...` の分岐だが、ずれたときの振る舞いを決める分岐なのでケースを立てる。

### テスト仕様表（`GetGuildRankings` / `GetUserRankings` 共通）

| # | 条件 | 図のパス | 期待結果 | 検証すべき呼び出し |
| --- | --- | --- | --- | --- |
| 1 | store の取得がエラー | `A→B→C→E1` | エラーを返す | 名前解決も total count も呼ばれない |
| 2 | `entries` が空 | `A→B→C→D→F→Z` | 空の `Rankings`、`TotalCount` は返る | **`ListXxxByIDs` が呼ばれない** |
| 3 | 名前解決がエラー | `…→D→G→E2` | エラーを返す | total count が呼ばれない |
| 4 | total count がエラー | `…→G→H→H2→F→E3` | エラーを返す | — |
| 5 | 正常系: 名前がマージされる | `…→H→H2→F→Z` | `Rankings` の各要素に Name が入る | `ListXxxByIDs` に entries の ID 配列が渡る |
| 6 | 正規化した値が store に渡る | 5 と同一 | 正常終了 | **`GetXxxRankings` に正規化後の offset/limit が渡る** |
| 7 | 名前マップに ID が無い | `…→H→H3→F→Z` | 該当エントリの `Name` は空文字。**件数は減らない** | — |

**ケース 6 について**: `limit=0 → DefaultRankingLimit`、`limit>Max → Max に丸め`、`offset<0 → 0`
といった正規化の**値**の検証は domain の責務。ここで検証するのは
「usecase が正規化を通した結果を store へ渡している」という**結線**だけなので、1 ケースで足りる。

---

## 2. 単一順位取得: `GetGuildRank` / `GetUserRank`

こちらも 2 メソッドで構造が同一。

```mermaid
flowchart TD
    A[開始] --> B[repo.GetGuild / GetUser<br/>名前を取得]
    B -- err --> E1((repo のエラーをそのまま返す))
    B -- ok --> C[rankingStore.GetXxxScore/Points]
    C -- err --> E2((get score エラー))
    C -- ok --> D{exists か}
    D -- No --> E3((ErrScoreNotFound / ErrPointsNotFound))
    D -- Yes --> F[rankingStore.GetXxxRank]
    F -- err --> E4((get rank エラー))
    F -- ok --> G[rankingStore.GetXxxTotalCount]
    G -- err --> E5((total count エラー))
    G -- ok --> Z([RankResult を返す])
```

### テスト仕様表（`GetGuildRank` / `GetUserRank` 共通）

| # | 条件 | 図のパス | 期待結果 | 検証すべき呼び出し |
| --- | --- | --- | --- | --- |
| 1 | `repo.GetXxx` がエラー | `A→B→E1` | repo のエラーを変換せず返す | store が一切呼ばれない |
| 2 | スコア/ポイント取得がエラー | `A→B→C→E2` | エラーを返す | rank / total count が呼ばれない |
| 3 | スコア/ポイント未登録（`exists=false`） | `…→C→D→E3` | `ErrScoreNotFound` / `ErrPointsNotFound` | 同上 |
| 4 | rank 取得がエラー | `…→D→F→E4` | エラーを返す | total count が呼ばれない |
| 5 | total count がエラー | `…→F→G→E5` | エラーを返す | — |
| 6 | 正常系 | `…→G→Z` | 名前・スコア・順位・総数がすべて入る | — |

---

## 3. `AddUserPoints`

ポイント加算。MySQL の更新（個人・ギルド・履歴）と Redis 反映用 outbox イベント登録を
**同一トランザクション内**で原子的に実行する。Redis 反映は outbox-worker が非同期に行うため、
このメソッドは順位を返さない。

```mermaid
flowchart TD
    A[開始] --> B{IsValidScore<br/>domain の判定}
    B -- No --> E1((ErrInvalidPoints))
    B -- Yes --> C[[DoInTx 境界に入る]]

    C -- 境界の確立/確定に失敗 --> E2((DoInTx のエラー))
    C -- fn 実行 --> D[repo.GetUser]
    D -- err --> E3((ErrUserNotFound 等をそのまま返す))
    D -- ok --> F[repo.GetUserGuildID]

    F -- err --> E4((ErrUserNotInGuild 等))
    F -- ok --> G[repo.GetUserPoints]

    G -- ErrPointsNotFound --> H[previousTotal = 0<br/>初回ユーザー]
    G -- その他の err --> E5((予期せぬエラー))
    G -- ok --> H2[previousTotal = 現在値]
    H --> K[repo.InsertUserPointHistory]
    H2 --> K

    K -- err --> E6((repo のエラー))
    K -- ok --> M[repo.IncrementUserPoints]
    M -- err --> E7((repo のエラー))
    M -- ok --> O[outboxRepo.InsertEvent]
    O -- err --> E8((insert outbox event エラー))
    O -- ok --> P[Result 構築・コミット]

    P --> Q[outboxNotifier.Notify<br/>**コミット後**に実行]
    Q -- err --> R[WARN ログのみ<br/>リクエストは成功させる]
    Q -- ok --> Z([Result を返す])
    R --> Z
```

**設計上の要点**（テストで守る不変条件）:

- `IsValidScore` の判定は `DoInTx` の**外**。不正入力でトランザクションを張らない
- ギルド集計（`GetGuildScore` / `InsertGuildScoreHistory` / `IncrementGuildScore`）は
  この tx で**行わない**。ホット行（1ギルド=1行）の排他ロックを同期リクエスト経路から
  除去するため outbox-worker へ非同期化した（[outbox-worker.md](outbox-worker.md)）。
  同期レスポンスも `GuildID` のみを返し、ギルドの previous/new total は返さない
- `Notify` は**コミット後**に呼ぶ。コミット前だと worker が空振りする
- `Notify` の失敗はリクエストを失敗させない。worker のポーリングがフォールバックになる

### テスト仕様表

| # | 条件 | 図のパス | 期待結果 | 検証すべき呼び出し |
| --- | --- | --- | --- | --- |
| 1 | `IsValidScore` が false | `A→B→E1` | `ErrInvalidPoints` | **`DoInTx` が呼ばれない** |
| 2 | `GetUser` がエラー（ユーザー不在） | `A→B→C→D→E3` | repo のエラーをそのまま返す | 以降の repo が呼ばれない |
| 3 | `GetUserGuildID` がエラー（ギルド未所属） | `…→D→F→E4` | `ErrUserNotInGuild` | 同上 |
| 4 | `GetUserPoints` が予期せぬエラー | `…→F→G→E5` | エラーを返す | 同上 |
| 5 | `InsertUserPointHistory` がエラー | `…→G→K→E6` | エラーを返す | 同上 |
| 6 | `IncrementUserPoints` がエラー | `…→K→M→E7` | エラーを返す | 同上 |
| 7 | `InsertEvent` がエラー | `…→M→O→E8` | エラーを返す | `Notify` が呼ばれない |
| 8 | 正常系: 既存ポイントあり | `…→H2→…→Q→Z` | `PreviousTotal` に現在値、`NewTotal` は加算後 | outbox に `RankingScoreAdded` が積まれる／`Notify` が呼ばれる／**ギルド集計の repo が呼ばれない** |
| 9 | 正常系: 初回ユーザー | `…→H→…→Z` | `PreviousTotal=0` | 同上 |
| 10 | `Notify` が失敗 | `…→Q→R→Z` | **リクエストは成功する**（エラーを返さない） | WARN ログが出る |

**`DoInTx` 自体のエラー（`E2`）について**: トランザクション境界の契約は
[transaction-boundary.md](transaction-boundary.md) が `Transactor` 自身のテストで担保する。
usecase 側は境界をモックしてよく、契約の検証を重複させない。
ただし「不正入力で境界に入らないこと」（ケース 1）は usecase 固有の責務なので、ここで検証する。

---

## 4. 本設計文書の作成で見つかった問題

`usecase` 層の文カバレッジは 100% であり、**この 4 件はいずれも数値では検知できない**ものだった。

| 種別 | 内容 | 対応 |
| --- | --- | --- |
| **層をまたぐ責務の重複** | `GetGuildRankings` に `limit=0 → 既定値`／`limit>Max → 丸め`／`offset<0 → 0` の 3 ケースがあった。これは `NormalizeLimit` / `NormalizeOffset` の境界値であり、[constants_test.go](../../internal/domain/ranking/constants_test.go) が既に 11 ケースで網羅している。usecase で検証すべきは「正規化を通した値を store へ渡している」という結線だけ | 表のケース 6（1 件）へ統合 |
| **対称性の欠如** | 上記 3 ケースは `GetGuildRankings` にだけあり、同一構造の `GetUserRankings` には 1 件も無かった。**片方だけにケースがある**状態 | 両者を同じケース構成に揃える |
| **層をまたぐ責務の重複** | `AddUserPoints` の「負数で `ErrInvalidPoints`」「最大値超過で `ErrInvalidPoints`」は同一パス（`A→B→E1`）。`IsValidScore` の境界は domain が 5 ケースで網羅済み | 表のケース 1 へ統合 |
| **同一パスの重複** | `AddUserPoints` の「境界値: `Points=MaxScore` でも加算成功」は正常系（ケース 8）と同一パス。`MaxScore` が有効値であることは domain の責務 | ケース 8 へ統合 |
| **テーブル駆動の形** | 5 つのテーブルすべてが `setup func(t, ctrl) ranking.Usecase` を持ち、モックの組み立て手順をテーブル側に書いていた | テーブルをデータのみにし、組み立てはランナーへ集約 |

**教訓**: gacha（Phase 4）では「テストが足りない」穴が出たが、ranking では逆に
**「下位層が担保済みの境界値を上位層で重ねてテストしている」過剰**が出た。
どちらもカバレッジ数値では見えない。層別の責務分担は図と表で突き合わせるしかない。

---

## 5. 図とテストの再突き合わせで見つかった問題

本文書とテストコードを改めて突き合わせた際に見つかったもの。いずれも文カバレッジ 100% のまま見逃されていた。

| 種別 | 内容 | 対応 |
| --- | --- | --- |
| **パスの欠落** | 名前解決の結果に entry の ID が含まれない分岐（`if _, ok := ...` の No 側）が、図・表・テストのいずれにも無かった。Redis の ZSet と MySQL がずれたときの振る舞いが未定義のまま | 図に `H` の分岐を追加し、表のケース 7 とテストを追加 |
| **検証の欠落** | ケース 10 の「検証すべき呼び出し」に **WARN ログが出る**とあるのに、テストは「エラーを返さない」ことしか見ていなかった。ログは「通知失敗を握り潰していない」ことの唯一の観測点なので、出ていないと検証が成立しない | このケースだけログ捕捉用の logger に差し替えて `assert` を追加 |

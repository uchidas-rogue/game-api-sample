# loadtest — k6 負荷試験

ROADMAP フェーズ3の負荷試験シナリオ一式。ローカルで API に負荷をかけ、ボトルネックを特定する。

## 前提

- k6 インストール済み（`brew install k6`）
- MySQL / Redis 起動済み（`docker compose -f deployments/docker-compose.yml up -d`）
- マイグレーション適用済み（`make db/migrate/up`）
- API と outbox-worker 起動済み（`make run` / `make run/outbox-worker`）
- gRPC シナリオを回す場合は gRPC サーバも起動済み（`make run/grpc`。既定 :9090、平文 h2c）

## 手順

```bash
# 1. データ投入（users/guilds/items/初期スコア）。Redis 反映まで込み
make load/seed                       # 既定 users=10000 guilds=100
make load/seed SEED_USERS=100000 SEED_GUILDS=500

# 2. 初期スコアを Redis に反映（load/seed 後は不要。Redis を飛ばしたときの復旧用）
make load/warm

# 3. 疎通確認（必ず最初に）
make load/smoke

# 4. 各シナリオ実行
make load/gacha        # 書き込み系（トランザクション+行ロック）
make load/points       # 書き込み系（スコア加算+ギルド集計+outbox）
make load/ranking      # 読み取り系（Redis ZSet 参照）
make load/grpc         # 読み取り系の gRPC 版（load/ranking と同条件）
```

> ランキング参照が `503 ranking is unavailable` を返す場合、Redis にランキングが構築されていない
> （`load/seed` / `load/warm` の未実行、または Redis の揮発）。`make load/warm` で再構築する。
> 揮発を空配列や 404 で誤魔化さないための仕様なので、閾値の緩和ではなく再構築で対処すること。
> 詳細は [docs/testing/ranking.md](../docs/testing/ranking.md) §0。
>
> **gRPC 側もまったく同じ**で、同じ状態が `codes.Unavailable` として現れる（HTTP 503 の対応コード）。
> `grpc-ranking.js` では check 失敗として集計されるため `checks` の閾値割れになる。
> ここでも対処は `make load/warm` による再構築で、閾値の緩和ではない。

## 負荷の調整（環境変数）

| 変数 | 意味 | 既定 |
|---|---|---|
| `RATE` | 維持する目標RPS | シナリオ毎（gacha/points=500, ranking=1000） |
| `START_RATE` | ramp 開始RPS | 50 |
| `RAMP` | ramp-up 時間 | 30s |
| `DURATION` | 維持時間 | 1m |
| `MAX_VUS` | 最大VU数 | 1000 |
| `BASE_URL` | 対象API（HTTP シナリオ） | http://localhost:8080 |
| `GRPC_ADDR` | 対象API（gRPC シナリオ。scheme 無しの host:port） | localhost:9090 |
| `SEED_USERS` / `SEED_GUILDS` | seed 規模（k6のID空間と一致させる） | 10000 / 100 |

例（stress: RPSを上げて限界点を探る）:

```bash
RATE=3000 RAMP=1m DURATION=3m make load/ranking
```

## シナリオ一覧

| ファイル | 種別 | 対象 |
|---|---|---|
| `smoke.js` | 疎通 | 全エンドポイントを低VUで一巡 |
| `gacha.js` | 書き込み | `POST /users/:id/gacha/multi` |
| `points.js` | 書き込み | `POST /users/:id/points` |
| `ranking.js` | 読み取り | `GET /rankings/*`, `GET /{users,guilds}/:id/ranking` |
| `grpc-ranking.js` | 読み取り（gRPC） | `game.ranking.v1.RankingService/{GetUserRankings,GetGuildRankings,GetUserRank,GetGuildRank}` |
| `watch_outbox.sh` | 計測 | outbox バックログ推移（負荷シナリオと併走） |

共通ヘルパ・負荷形状は `lib/common.js` に集約。

### `grpc-ranking.js`（HTTP 版との比較用）

`ranking.js` と**対**になるシナリオ。gRPC を足したことの効果を実測するのが目的なので、
比較が成立するように次を HTTP 版と一致させてある:

- 負荷形状: 同じ `arrivalScenario({ rate: 1000 })`（ramp・維持・減衰まで同一）
- 呼び出しの内訳: 一覧 35% + 35%、個別順位 15% + 15%
- 対象ユースケース: 同じ usecase を HTTP delivery と gRPC delivery が共有している

したがって **片方だけ `RATE` や比率を変えると比較にならない**。変えるときは両方揃えること。
`RATE=3000 make load/grpc` と `RATE=3000 make load/ranking` を並べて、`grpc_req_duration` と
`http_req_duration` の p95/p99、および実 RPS を突き合わせる。

`.proto` は `proto/` の正本をそのまま読む（`client.load(['../proto'], 'game/ranking/v1/ranking.proto')`）。
import パスは **スクリプトファイルからの相対**で解決されるため、どの cwd から `k6 run` しても通る。
`loadtest/` 配下に `.proto` の写しを置かないこと。

**接続は VU ごとに1回だけ張る。** gRPC は1本の接続を多重化して使うため、毎イテレーションで
`client.connect()` すると接続確立（TCP + HTTP/2 ハンドシェイク）のコストを測ってしまい、
HTTP 版との比較が壊れる。

#### streaming（`WatchUserRankings`）を含めていない理由

このシナリオは unary の4 RPC だけを対象にしている。`WatchUserRankings` は
**「秒間 N リクエスト」という負荷形状（`ramping-arrival-rate`）と噛み合わない**ためである。
ストリームは1回張って接続を保持し続けるものなので、到達レートで刻む負荷モデルに乗らず、
`grpc_req_duration` にも1本ぶんの値しか出ない。測るべき指標（同時購読数・push 遅延・
サーバ側のファンアウトコスト）が別物なので、必要になったら
`constant-vus` + 購読保持の専用シナリオとして別ファイルに切る。

## outbox バックログの計測（`make load/watch`）

`points` シナリオはギルド集計を outbox-worker へ非同期化しているため、**API のレイテンシが良好でも
非同期側に積み残している可能性がある**。k6 の数値だけでは見えないので、バックログを直接観測する。

負荷シナリオとは別ターミナルで併走させる:

```bash
# T1: 計測開始（先に回す）
make load/watch WATCH_OUT=run1.csv WATCH_DURATION=600

# T2: 負荷（ramp30s + 維持60s + 減衰10s ≒ 100秒）
make load/points
```

**負荷終了後も計測は止めないこと。** テールの消化速度が最重要の観測対象。

| 変数 | 意味 | 既定 |
|---|---|---|
| `WATCH_OUT` | 出力 CSV | `outbox_metrics.csv` |
| `WATCH_DURATION` | 計測秒数 | 900 |
| `WATCH_INTERVAL` | サンプリング間隔（秒） | 1 |

### 計測オーバーヘッドについて

**計測が対象を歪めないことが前提**なので、`docker exec` は計測全体で1回だけ起動し、常駐した
mysql クライアントの標準入力にクエリを流し込む。サンプリング毎に `docker exec` すると
Docker Desktop for Mac では1回あたり約 110ms かかり（クエリ本体は約 30ms）、毎秒払うと
MySQL と同じ VM 内でプロセス生成が繰り返されて計測対象の API レイテンシに影響する。

それでも影響が疑われる場合は `WATCH_INTERVAL=5` などで間隔を広げる。バックログの傾向を
見るだけなら 5 秒間隔で十分。

### 出力列と読み方

| 列 | 意味 |
|---|---|
| `pending` | 未処理イベント数（= `guild_scores` に未反映の加算） |
| `processed_delta` | 間隔あたりの消化数（= worker の実スループット） |
| `guild_scores_sum` | `guild_scores.score` 総和（反映の進捗） |

| 観測 | 判定 |
|---|---|
| 負荷中に `pending` が単調増加 | worker が生産レートに追いつかない |
| `processed_delta` が頭打ち | その値が worker のスループット上限 |
| 負荷終了後に `pending` が平坦なまま | 通知が尽き `OUTBOX_POLL_INTERVAL` 待ち（既定 10m）。`runOnce` は1回 `OUTBOX_BATCH_SIZE` 件で返るため、テールは「間隔ごとに batch 件」しか進まない |
| 負荷終了後に継続的に減り 0 になる | 遅延は実質なし |

`OUTBOX_POLL_INTERVAL=1s make run/outbox-worker` で worker を起動し直して再計測すると、
テールの消化が poll 間隔律速かどうかを切り分けられる。

### 整合性の最終確認（`pending = 0` になった後）

```bash
docker exec -e MYSQL_PWD=game game-api-mysql mysql -u game game_db -e "
SELECT
  (SELECT COUNT(*) FROM user_point_histories WHERE reason='loadtest') AS user_hist,
  (SELECT COUNT(*) FROM guild_score_histories) AS guild_hist,
  (SELECT SUM(points) FROM user_point_histories WHERE reason='loadtest') AS user_pt_sum,
  (SELECT SUM(score) FROM guild_scores) AS guild_score_sum;"
```

件数・合計が一致すれば outbox の exactly-once が効いている（seed の初期スコア分はオフセットとして差し引く）。

## 合否基準（thresholds）

HTTP シナリオ（`lib/common.js` の `defaultThresholds`）:

- エラー率 `http_req_failed < 1%`
- レイテンシ `p95 < 200ms` / `p99 < 500ms`

gRPC シナリオ（同 `defaultGrpcThresholds`）— 水準は同じで、メトリクス名だけ読み替える:

- エラー率 `checks > 99%`（`grpc_req_failed` に相当するメトリクスが無いため、
  `invoke` の status を `check` した成功率で見る）
- レイテンシ `grpc_req_duration` の `p95 < 200ms` / `p99 < 500ms`
- 到達性 `grpc_calls > 0`（下記の「サンプル0件の空パス」対策）

**gRPC シナリオに `defaultThresholds` を流用しないこと。** gRPC 呼び出しは `http_req_*` を
一切出さないので、閾値が「対象0件」のまま無条件でパスし、遅延もエラーも検出しないまま緑になる。

### `grpc_calls` が要る理由（サンプル0件の空パス）

k6 は**サンプル0件のメトリクスに対する `rate` / `p(95)` 閾値を無条件でパスさせる**。
gRPC ではこれが実害になる: サーバが落ちていると `client.connect()` が毎イテレーション例外を投げ、
`invoke` まで到達しないため `checks` も `grpc_req_duration` も0件のままになる。
結果、**全イテレーション失敗なのに exit code 0（緑）**で終わる。

そこで `invoke` の応答を受け取るたびに増やす Counter `grpc_calls` を置き、
`count>0` を閾値にしてある。Counter の `count` はサンプル0件でも正しく判定される。

> `grpc_req_duration` に `count>0` は付けられない。k6 の Trend が対応する集計は
> `avg` / `min` / `max` / `med` / `p` のみで、`count` を指定すると閾値の設定エラーになる。

新しい gRPC シナリオを足すときは、**`invoke` のたびに `grpcCalls.add(1)` を呼ぶこと**
（`grpc-ranking.js` の `expectOK` が手本）。忘れると常に閾値割れするので、静かに壊れることはない。

閾値超過で k6 は exit code≠0 を返す（CI 連携時の合否判定に使える）。

## 結果の見方

k6 の要約（`http_req_duration` の p95/p99、`http_reqs` の実RPS、`http_req_failed`。
gRPC なら `grpc_req_duration` と `checks`）と、
同時刻の CloudWatch メトリクス（ECS CPU / Aurora Deadlocks・CPU / ElastiCache Evictions 等）を
突き合わせてボトルネックを特定する。詳細は `ROADMAP.md` フェーズ3参照。

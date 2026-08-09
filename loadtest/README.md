# loadtest — k6 負荷試験

ROADMAP フェーズ3の負荷試験シナリオ一式。ローカルで API に負荷をかけ、ボトルネックを特定する。

## 前提

- k6 インストール済み（`brew install k6`）
- MySQL / Redis 起動済み（`docker compose -f deployments/docker-compose.yml up -d`）
- マイグレーション適用済み（`make db/migrate/up`）
- API と outbox-worker 起動済み（`make run` / `make run/outbox-worker`）

## 手順

```bash
# 1. データ投入（users/guilds/items/初期スコア）
make load/seed                       # 既定 users=10000 guilds=100
make load/seed SEED_USERS=100000 SEED_GUILDS=500

# 2. 初期スコアを Redis に反映（ランキング参照を温める）
make load/warm

# 3. 疎通確認（必ず最初に）
make load/smoke

# 4. 各シナリオ実行
make load/gacha        # 書き込み系（トランザクション+行ロック）
make load/points       # 書き込み系（スコア加算+ギルド集計+outbox）
make load/ranking      # 読み取り系（Redis ZSet 参照）
```

## 負荷の調整（環境変数）

| 変数 | 意味 | 既定 |
|---|---|---|
| `RATE` | 維持する目標RPS | シナリオ毎（gacha/points=500, ranking=1000） |
| `START_RATE` | ramp 開始RPS | 50 |
| `RAMP` | ramp-up 時間 | 30s |
| `DURATION` | 維持時間 | 1m |
| `MAX_VUS` | 最大VU数 | 1000 |
| `BASE_URL` | 対象API | http://localhost:8080 |
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

共通ヘルパ・負荷形状は `lib/common.js` に集約。

## 合否基準（thresholds）

- エラー率 `http_req_failed < 1%`
- レイテンシ `p95 < 200ms` / `p99 < 500ms`

閾値超過で k6 は exit code≠0 を返す（CI 連携時の合否判定に使える）。

## 結果の見方

k6 の要約（`http_req_duration` の p95/p99、`http_reqs` の実RPS、`http_req_failed`）と、
同時刻の CloudWatch メトリクス（ECS CPU / Aurora Deadlocks・CPU / ElastiCache Evictions 等）を
突き合わせてボトルネックを特定する。詳細は `ROADMAP.md` フェーズ3参照。

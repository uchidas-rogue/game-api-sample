# 負荷試験系（k6 + seed）。ROADMAP フェーズ3。
# 詳細は loadtest/README.md を参照。

.PHONY: load/seed load/warm load/smoke load/gacha load/points load/ranking

K6           ?= k6
LOADTEST_DIR ?= loadtest
BASE_URL     ?= http://localhost:8080
SEED_USERS   ?= 10000
SEED_GUILDS  ?= 100

# k6 へ渡す共通環境変数（ID空間を seed 規模に一致させる）。
K6_ENV = BASE_URL=$(BASE_URL) USERS=$(SEED_USERS) GUILDS=$(SEED_GUILDS)

## 負荷試験データ投入（users/guilds/items/初期スコア）
load/seed:
	go run ./cmd/batch -seed -users=$(SEED_USERS) -guilds=$(SEED_GUILDS)

## 初期スコアを Redis に反映（ランキング参照を温める）
load/warm:
	go run ./cmd/batch -sync-rankings

## smoke（全エンドポイントを低VUで一巡し疎通確認）
load/smoke:
	$(K6_ENV) $(K6) run $(LOADTEST_DIR)/smoke.js

## gacha 負荷（書き込み系: トランザクション+行ロック）
load/gacha:
	$(K6_ENV) $(K6) run $(LOADTEST_DIR)/gacha.js

## points 負荷（書き込み系: スコア加算+ギルド集計+outbox）
load/points:
	$(K6_ENV) $(K6) run $(LOADTEST_DIR)/points.js

## ranking 負荷（読み取り系: Redis ZSet 参照）
load/ranking:
	$(K6_ENV) $(K6) run $(LOADTEST_DIR)/ranking.js

# 負荷試験系（k6 + seed）。ROADMAP フェーズ3。
# 詳細は loadtest/README.md を参照。

.PHONY: load/seed load/warm load/smoke load/gacha load/points load/ranking load/grpc load/watch

K6           ?= k6
LOADTEST_DIR ?= loadtest
BASE_URL     ?= http://localhost:8080
# gRPC は scheme を持たない host:port。平文 h2c で listen する（make run/grpc）。
GRPC_ADDR    ?= localhost:9090
SEED_USERS   ?= 10000
SEED_GUILDS  ?= 100

# k6 へ渡す共通環境変数（ID空間を seed 規模に一致させる）。
K6_ENV = BASE_URL=$(BASE_URL) GRPC_ADDR=$(GRPC_ADDR) USERS=$(SEED_USERS) GUILDS=$(SEED_GUILDS)

## 負荷試験データ投入（users/guilds/items/初期スコア）。Redis 反映まで行う
load/seed:
	go run ./cmd/batch -seed -users=$(SEED_USERS) -guilds=$(SEED_GUILDS)

## 初期スコアを Redis に反映（ランキング参照を温める。load/seed 後は不要）
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

# load/ranking と同じ負荷形状・同じ呼び出し比率にしてある（HTTP と gRPC の比較が目的）。
# 片方だけ RATE や比率を変えると比較にならないので、変えるときは両方を揃えること。
## gRPC ranking 負荷（読み取り系: load/ranking の gRPC 版。同条件で比較する）
load/grpc:
	$(K6_ENV) $(K6) run $(LOADTEST_DIR)/grpc-ranking.js

# outbox バックログ計測。負荷シナリオと別ターミナルで併走させる。
WATCH_OUT      ?= outbox_metrics.csv
WATCH_DURATION ?= 900
WATCH_INTERVAL ?= 1

## outbox バックログ推移を CSV 記録（負荷シナリオと併走させる）
load/watch:
	$(LOADTEST_DIR)/watch_outbox.sh $(WATCH_OUT) $(WATCH_DURATION) $(WATCH_INTERVAL)

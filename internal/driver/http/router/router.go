// Package router はEchoのルーティング定義を集約する。
package router

import (
	"github.com/labstack/echo/v4"

	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/gacha"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/health"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/ranking"
)

// Handlers はルーティングに必要な全ハンドラを束ねる構造体。
// 機能追加時はこの構造体にハンドラを追加していく。
//
// 全フィールドが埋まっていることを前提とする。部分的に nil のまま登録する構成は
// サポートしない（コンポジションルート internal/di が常に全ハンドラを組み立てる）。
type Handlers struct {
	Health  *healthhandler.Handler
	Gacha   *gachahandler.Handler
	Ranking *rankinghandler.Handler
}

// Register はEchoインスタンスにルーティングを登録する。
//
// nil ガードは置かない。以前は Gacha / Ranking だけを nil チェックし Health はしない
// 非対称な状態だった。nil を許すと「一部のエンドポイントだけ 404 になる」設定ミスが
// 起動時に素通りし、リクエストが来て初めて分かる。全ハンドラ必須にすることで、
// 組み立て漏れは nil 参照として起動直後に露見する。
func Register(e *echo.Echo, h Handlers) {
	// ヘルスチェック
	e.GET("/healthz", h.Health.Check)

	// ガチャ
	e.POST("/users/:userID/gacha/multi", h.Gacha.Multi)

	// ギルドランキング（参照系のみ。書き込みは個人スコア送信からの自動合算）
	e.GET("/rankings/guilds", h.Ranking.GetGuildRankings)
	e.GET("/guilds/:guildID/ranking", h.Ranking.GetGuildRank)

	// 個人ポイントランキング（書き込み起点。所属ギルドにもリアルタイム合算）
	e.POST("/users/:userID/points", h.Ranking.AddUserPoints)
	e.GET("/rankings/users", h.Ranking.GetUserRankings)
	e.GET("/users/:userID/ranking", h.Ranking.GetUserRank)
}

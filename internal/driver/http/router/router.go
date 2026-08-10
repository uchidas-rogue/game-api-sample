// Package router はEchoのルーティング定義を集約する。
package router

import (
	"errors"
	"fmt"
	"strings"

	"github.com/labstack/echo/v4"

	gachahandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/gacha"
	healthhandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/health"
	rankinghandler "github.com/uchidas-rogue/game-api-sample/internal/driver/http/ranking"
)

// ErrMissingHandler は Handlers に nil のフィールドがあることを表す。
// DI（コンポジションルート）の組み立て漏れなので、起動を中止する種類のエラー。
var ErrMissingHandler = errors.New("router: handler is not assembled")

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
// ハンドラが1つでも nil なら、ルートを登録せずエラーを返す。
//
// 以前は Gacha / Ranking だけを nil チェックして登録をスキップし、Health はしない
// 非対称な状態だった。スキップすると「一部のエンドポイントだけ 404 になる」設定ミスが
// 起動後も表面化しないため、全ハンドラ必須に変えている。
//
// ただし nil チェックを単に外すだけでは fail-fast にならない。`h.Gacha.Multi` は
// メソッド値の生成であり、レシーバが nil でもここではデリファレンスされない。
// 登録もサーバ起動も成功し、panic は最初のリクエストまで遅延したうえ
// middleware.Recover() が 500 に丸めてしまう。だから明示的に検査する。
func Register(e *echo.Echo, h Handlers) error {
	if err := h.validate(); err != nil {
		return err
	}

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

	return nil
}

// validate は全ハンドラが組み立てられていることを検査する。
// 欠けたフィールド名をすべて挙げる（1つずつ直して再起動する往復を避けるため）。
// フィールドを追加したらここにも追加する。
func (h Handlers) validate() error {
	fields := []struct {
		name     string
		assigned bool
	}{
		{"Health", h.Health != nil},
		{"Gacha", h.Gacha != nil},
		{"Ranking", h.Ranking != nil},
	}

	var missing []string
	for _, f := range fields {
		if !f.assigned {
			missing = append(missing, f.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrMissingHandler, strings.Join(missing, ", "))
}

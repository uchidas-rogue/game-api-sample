package repository

import (
	"github.com/uchidas-rogue/game-api-sample/internal/infrastructure/mysql/sqlc"
	gachausecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/gacha"
)

// NewGachaRepositoryWithQuerier はテスト専用のコンストラクタ。
// querier ファクトリを直接差し替えることで、sqlc.Querier のモックを注入できる。
// tx パラメータが渡されても、ファクトリ側でモック Querier を返すため tx の中身は参照されない。
func NewGachaRepositoryWithQuerier(q sqlc.Querier) *GachaRepository {
	return &GachaRepository{
		querier: func(_ gachausecase.Tx) (sqlc.Querier, error) {
			return q, nil
		},
	}
}

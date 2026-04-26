// Package health はヘルスチェックのユースケースを提供する。
package health

//go:generate mockgen -source=usecase.go -destination=mock/mock_usecase.go -package=mock_health

import (
	"context"

	healthdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/health"
)

// Usecase はヘルスチェックのユースケースインターフェース。
// 将来的にDB/Redisの疎通確認を追加する際もこのインターフェースで吸収する。
type Usecase interface {
	Check(ctx context.Context) (healthdomain.HealthStatus, error)
}

// usecase はUsecaseの具象実装。
type usecase struct{}

// NewUsecase はUsecaseの新しいインスタンスを生成する。
func NewUsecase() Usecase {
	return &usecase{}
}

// Check は現在のサービス稼働状態を返す。
// 現状は無条件にStatusOKを返すが、フェーズ2以降で依存リソースの疎通確認を追加する想定。
func (u *usecase) Check(_ context.Context) (healthdomain.HealthStatus, error) {
	return healthdomain.StatusOK, nil
}

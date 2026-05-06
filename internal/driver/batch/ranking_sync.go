// Package batch はバッチ処理を提供する。
package batch

import (
	"context"
	"fmt"
	"log/slog"

	outboxdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/outbox"
	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	outboxusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/outbox"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// RankingSyncer は DB のランキング状態を Redis に焼き直すバッチ処理。
//
// 整合性ポリシー:
//   - DB 読み取りと outbox イベントの processed マークを単一トランザクション内で実施する
//   - Redis への SET（外部副作用）は COMMIT 後に行う
//   - これにより、バッチが Redis に焼き直したスナップショットの「カバー範囲」と
//     outbox の processed フラグが一致し、worker による同じイベントの二重適用を防ぐ
type RankingSyncer struct {
	repo         rankingusecase.Repository
	outboxRepo   outboxusecase.Repository
	rankingStore rankingusecase.RankingStore
	tx           shared.Transactor
	logger       *slog.Logger
}

// NewRankingSyncer は RankingSyncer を生成する。
func NewRankingSyncer(
	repo rankingusecase.Repository,
	outboxRepo outboxusecase.Repository,
	rankingStore rankingusecase.RankingStore,
	tx shared.Transactor,
	logger *slog.Logger,
) *RankingSyncer {
	return &RankingSyncer{
		repo:         repo,
		outboxRepo:   outboxRepo,
		rankingStore: rankingStore,
		tx:           tx,
		logger:       logger,
	}
}

// SyncAll は全ランキングを同期する。
// トランザクション内で DB スナップショット取得と outbox の processed マークを行い、
// COMMIT 後に Redis へ反映する。
func (s *RankingSyncer) SyncAll(ctx context.Context) error {
	s.logger.InfoContext(ctx, "starting ranking sync")

	var (
		guildScores []rankingdomain.GuildScore
		userPoints  []rankingdomain.UserPoint
		markedRows  int64
		maxID       uint64
	)

	if err := s.tx.DoInTx(ctx, func(tx shared.Tx) error {
		// outbox の現在の最大 ID をスナップショット境界として先に取得する。
		// REPEATABLE READ により後続の SELECT はこの境界と整合する。
		id, err := s.outboxRepo.GetMaxID(ctx, tx)
		if err != nil {
			return fmt.Errorf("get max outbox id: %w", err)
		}
		maxID = id

		gs, err := s.repo.ListAllGuildScores(ctx, tx)
		if err != nil {
			return fmt.Errorf("list all guild scores: %w", err)
		}
		guildScores = gs

		up, err := s.repo.ListAllUserPoints(ctx, tx)
		if err != nil {
			return fmt.Errorf("list all user points: %w", err)
		}
		userPoints = up

		// スナップショット境界までの ranking 系 pending イベントを processed にマークする。
		// ranking 以外のイベント (将来追加されるドメインのイベント) を巻き添えにしないよう
		// event_type を明示的に絞り込む。maxID == 0 の場合（空テーブル）でもクエリは安全（0 件 UPDATE）。
		rows, err := s.outboxRepo.MarkProcessedUpTo(ctx, tx, maxID, outboxdomain.EventTypeRankingScoreAdded)
		if err != nil {
			return fmt.Errorf("mark outbox processed up to %d: %w", maxID, err)
		}
		markedRows = rows
		return nil
	}); err != nil {
		return fmt.Errorf("ranking sync tx: %w", err)
	}

	s.logger.InfoContext(ctx, "ranking sync snapshot taken",
		slog.Uint64("outbox_max_id", maxID),
		slog.Int64("outbox_marked_processed", markedRows),
		slog.Int("guild_count", len(guildScores)),
		slog.Int("user_count", len(userPoints)),
	)

	// COMMIT 後に Redis へ反映。個別 SET の失敗はログのみで処理継続（既存ポリシー踏襲）。
	for _, gs := range guildScores {
		if err := s.rankingStore.SetGuildScore(ctx, gs.GuildID, gs.Score); err != nil {
			s.logger.ErrorContext(ctx, "failed to set guild score",
				slog.Int64("guild_id", gs.GuildID),
				slog.Any("error", err))
		}
	}
	for _, up := range userPoints {
		if err := s.rankingStore.SetUserPoints(ctx, up.UserID, up.Points); err != nil {
			s.logger.ErrorContext(ctx, "failed to set user points",
				slog.Int64("user_id", up.UserID),
				slog.Any("error", err))
		}
	}

	s.logger.InfoContext(ctx, "ranking sync completed",
		slog.Int("synced_guilds", len(guildScores)),
		slog.Int("synced_users", len(userPoints)),
	)
	return nil
}

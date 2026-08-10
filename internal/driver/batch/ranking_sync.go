// Package batch はバッチ処理を提供する。
package batch

import (
	"context"
	"fmt"
	"log/slog"

	rankingdomain "github.com/uchidas-rogue/game-api-sample/internal/domain/ranking"
	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
	"github.com/uchidas-rogue/game-api-sample/internal/usecase/shared"
)

// RankingSyncer は MySQL のランキング状態（guild_scores / user_points）を Redis ZSet に
// 焼き直す「揃え直し（rebuild）専用」バッチ。Redis 揮発時の復旧に用いる。
//
// 設計方針:
//   - MySQL の guild_scores / user_points を唯一の source of truth とし、その値で Redis を
//     上書き（SET）する。guild_scores は outbox-worker がイベントを exactly-once に適用して
//     維持するため、本バッチは outbox イベントの processed マークには一切関与しない。
//   - スナップショット一貫性のため、guild_scores と user_points の読み取りは単一トランザクション
//     （REPEATABLE READ）で行う。Redis への SET は COMMIT 後に行う。
//   - 個別 SET の失敗ではループを打ち切らず、届く分は届ける。ただし 1 件でも失敗したら
//     SyncAll はエラーを返す（呼び出し元の cmd/batch が非ゼロ終了する）。復旧できていない状態を
//     「成功」として報告すると、Redis が空のままであることに運用側が気づけないため。
//   - 注意: worker が稼働中に本バッチを走らせると、スナップショット取得後に worker が反映した
//     数件が SET で上書きされ Redis 側で一時的に欠落しうる。ただし MySQL は常に正しく、次回の
//     再構築で自己修復する。原則として揮発復旧など書き込みが静穏な状況で実行する。
type RankingSyncer struct {
	repo         rankingusecase.Repository
	rankingStore rankingusecase.RankingStore
	tx           shared.Transactor
	logger       *slog.Logger
}

// NewRankingSyncer は RankingSyncer を生成する。
func NewRankingSyncer(
	repo rankingusecase.Repository,
	rankingStore rankingusecase.RankingStore,
	tx shared.Transactor,
	logger *slog.Logger,
) *RankingSyncer {
	return &RankingSyncer{
		repo:         repo,
		rankingStore: rankingStore,
		tx:           tx,
		logger:       logger,
	}
}

// SyncAll は MySQL のランキング状態を Redis に焼き直す。
// 単一トランザクションで guild_scores / user_points のスナップショットを取得し、
// COMMIT 後に Redis へ SET する。
func (s *RankingSyncer) SyncAll(ctx context.Context) error {
	s.logger.InfoContext(ctx, "starting ranking sync")

	var (
		guildScores []rankingdomain.GuildScore
		userPoints  []rankingdomain.UserPoint
	)

	if err := s.tx.DoInTx(ctx, func(tx shared.Tx) error {
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
		return nil
	}); err != nil {
		return fmt.Errorf("ranking sync tx: %w", err)
	}

	s.logger.InfoContext(ctx, "ranking sync snapshot taken",
		slog.Int("guild_count", len(guildScores)),
		slog.Int("user_count", len(userPoints)),
	)

	// COMMIT 後に Redis へ反映する。
	// 個別 SET が失敗してもループは止めない（届く分は届ける）が、失敗を数えておき、
	// 1 件でもあれば最後にエラーを返す。本バッチは Redis 揮発からの復旧手段なので、
	// 復旧できていないのに exit 0 を返すと運用側が Redis の欠落に気づけない。
	var (
		failedGuilds int
		failedUsers  int
		firstErr     error
	)
	recordFailure := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, gs := range guildScores {
		if err := s.rankingStore.SetGuildScore(ctx, gs.GuildID, gs.Score); err != nil {
			failedGuilds++
			recordFailure(err)
			s.logger.ErrorContext(ctx, "failed to set guild score",
				slog.Int64("guild_id", gs.GuildID),
				slog.Any("error", err))
		}
	}
	for _, up := range userPoints {
		if err := s.rankingStore.SetUserPoints(ctx, up.UserID, up.Points); err != nil {
			failedUsers++
			recordFailure(err)
			s.logger.ErrorContext(ctx, "failed to set user points",
				slog.Int64("user_id", up.UserID),
				slog.Any("error", err))
		}
	}

	if firstErr != nil {
		return fmt.Errorf(
			"ranking sync incomplete: redis set failed (guilds %d/%d, users %d/%d): %w",
			failedGuilds, len(guildScores), failedUsers, len(userPoints), firstErr,
		)
	}

	s.logger.InfoContext(ctx, "ranking sync completed",
		slog.Int("synced_guilds", len(guildScores)),
		slog.Int("synced_users", len(userPoints)),
	)
	return nil
}

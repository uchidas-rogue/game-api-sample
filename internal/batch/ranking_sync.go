// Package batch はバッチ処理を提供する。
package batch

import (
	"context"
	"fmt"
	"log/slog"

	rankingusecase "github.com/uchidas-rogue/game-api-sample/internal/usecase/ranking"
)

// RankingSyncer はRedisとDBのランキングを同期するバッチ処理。
type RankingSyncer struct {
	repo         rankingusecase.Repository
	rankingStore rankingusecase.RankingStore
	logger       *slog.Logger
}

// NewRankingSyncer は RankingSyncer を生成する。
func NewRankingSyncer(
	repo rankingusecase.Repository,
	rankingStore rankingusecase.RankingStore,
	logger *slog.Logger,
) *RankingSyncer {
	return &RankingSyncer{
		repo:         repo,
		rankingStore: rankingStore,
		logger:       logger,
	}
}

// SyncAll は全ランキングを同期する。
func (s *RankingSyncer) SyncAll(ctx context.Context) error {
	if err := s.SyncGuildRankings(ctx); err != nil {
		return fmt.Errorf("sync guild rankings: %w", err)
	}
	if err := s.SyncUserRankings(ctx); err != nil {
		return fmt.Errorf("sync user rankings: %w", err)
	}
	return nil
}

// SyncGuildRankings はギルドランキングを同期する。
func (s *RankingSyncer) SyncGuildRankings(ctx context.Context) error {
	s.logger.InfoContext(ctx, "starting guild rankings sync")

	scores, err := s.repo.ListAllGuildScores(ctx, nil)
	if err != nil {
		return fmt.Errorf("list all guild scores: %w", err)
	}

	s.logger.InfoContext(ctx, "fetched guild scores from DB", slog.Int("count", len(scores)))

	for _, gs := range scores {
		if err := s.rankingStore.SetGuildScore(ctx, gs.GuildID, gs.Score); err != nil {
			s.logger.ErrorContext(ctx, "failed to set guild score",
				slog.Int64("guild_id", gs.GuildID),
				slog.String("error", err.Error()))
			continue
		}
	}

	s.logger.InfoContext(ctx, "completed guild rankings sync", slog.Int("synced", len(scores)))
	return nil
}

// SyncUserRankings はユーザーランキングを同期する。
func (s *RankingSyncer) SyncUserRankings(ctx context.Context) error {
	s.logger.InfoContext(ctx, "starting user rankings sync")

	points, err := s.repo.ListAllUserPoints(ctx, nil)
	if err != nil {
		return fmt.Errorf("list all user points: %w", err)
	}

	s.logger.InfoContext(ctx, "fetched user points from DB", slog.Int("count", len(points)))

	for _, up := range points {
		if err := s.rankingStore.SetUserPoints(ctx, up.UserID, up.Points); err != nil {
			s.logger.ErrorContext(ctx, "failed to set user points",
				slog.Int64("user_id", up.UserID),
				slog.String("error", err.Error()))
			continue
		}
	}

	s.logger.InfoContext(ctx, "completed user rankings sync", slog.Int("synced", len(points)))
	return nil
}

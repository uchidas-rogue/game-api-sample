// Package seed は負荷試験・ローカル開発用のダミーデータ投入を担う。
//
// 本パッケージは業務ロジックではなく開発/運用ツール（dev tooling）であり、
// 大量行の一括投入という性能要件から sqlc の単行クエリではなく
// 複数行 INSERT の生 SQL を用いる（infrastructure 層の責務として許容）。
// 冪等性のため全て ON DUPLICATE KEY UPDATE でアップサートし、再実行を安全にする。
package seed

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"

	gachadomain "github.com/uchidas-rogue/game-api-sample/internal/domain/gacha"
)

// 投入既定値・調整パラメータ（マジックナンバー禁止対応）。
const (
	// DefaultUsers は投入するユーザー数の既定値。
	DefaultUsers = 10000
	// DefaultGuilds は投入するギルド数の既定値。
	DefaultGuilds = 100
	// DefaultGemNum は各ユーザーに付与する石残高。
	// 負荷試験中に石不足(402)でリクエストが弾かれ計測が歪むのを防ぐため大きめに固定する。
	// items.gem_num は INT のため上限(約21.4億)未満に収める。
	DefaultGemNum = 1_000_000_000

	// insertChunkSize は1回の INSERT 文にまとめる行数。
	// プレースホルダ上限とメモリのバランスで決める。
	insertChunkSize = 1000

	// randSeed は投入データを再現可能にするための固定シード。
	// 現在時刻を種にしないことで、同一パラメータなら毎回同じデータを得る。
	// math/rand/v2 の PCG は 128bit の種を取るため、2 つ目は固定の補助値。
	randSeed    = 20260720
	randSeedAux = 0x9E3779B97F4A7C15

	// maxInitialUserPoints は初期投入する個人ポイントの上限（0〜この値の一様乱数）。
	maxInitialUserPoints = 100000
)

// Seeder は MySQL へダミーデータを投入する。
type Seeder struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSeeder は Seeder を生成する。
func NewSeeder(db *sql.DB, logger *slog.Logger) *Seeder {
	return &Seeder{db: db, logger: logger}
}

// Params は投入規模を指定する。
type Params struct {
	Users  int
	Guilds int
	GemNum int
}

// withDefaults は未指定(0以下)の項目を既定値で補う。
func (p Params) withDefaults() Params {
	if p.Users <= 0 {
		p.Users = DefaultUsers
	}
	if p.Guilds <= 0 {
		p.Guilds = DefaultGuilds
	}
	if p.GemNum <= 0 {
		p.GemNum = DefaultGemNum
	}
	return p
}

// catalogItem はガチャアイテムマスタの1件。
type catalogItem struct {
	name   string
	rarity int
	weight int
}

// itemCatalog は投入するアイテムマスタの固定カタログを返す。
// weight はレアリティが上がるほど小さく（排出されにくく）設定している。
//
// パッケージスコープの変数ではなく関数にしているのは、呼び出し側が
// 要素を書き換えても他の呼び出しに波及させないため（AGENTS.md §2 / gochecknoglobals）。
func itemCatalog() []catalogItem {
	return []catalogItem{
		{"かけらの石", gachadomain.RarityN, 300},
		{"やくそう", gachadomain.RarityN, 300},
		{"どうのつるぎ", gachadomain.RarityN, 250},
		{"きぬのローブ", gachadomain.RarityN, 250},
		{"てつのたて", gachadomain.RarityR, 120},
		{"はがねのつるぎ", gachadomain.RarityR, 120},
		{"まほうのぼうし", gachadomain.RarityR, 100},
		{"ちからのゆびわ", gachadomain.RarityR, 100},
		{"ほのおの剣", gachadomain.RaritySR, 40},
		{"こおりの杖", gachadomain.RaritySR, 40},
		{"いかずちの槍", gachadomain.RaritySR, 30},
		{"せいなるたて", gachadomain.RaritySR, 30},
		{"でんせつの剣", gachadomain.RaritySSR, 8},
		{"りゅうおうの杖", gachadomain.RaritySSR, 6},
		{"ゆうしゃのよろい", gachadomain.RaritySSR, 6},
	}
}

// Seed はアイテム/ギルド/ユーザー/ギルドメンバー/初期スコアを投入する。
// FK 制約を満たす順序（items,guilds → users → guild_members,scores）で実行する。
func (s *Seeder) Seed(ctx context.Context, p Params) error {
	p = p.withDefaults()
	s.logger.InfoContext(ctx, "seeding start",
		slog.Int("users", p.Users),
		slog.Int("guilds", p.Guilds),
		slog.Int("gem_num", p.GemNum),
	)
	rng := rand.New(rand.NewPCG(randSeed, randSeedAux))

	if err := s.seedItems(ctx); err != nil {
		return fmt.Errorf("seed items: %w", err)
	}
	if err := s.seedGuilds(ctx, p.Guilds); err != nil {
		return fmt.Errorf("seed guilds: %w", err)
	}
	if err := s.seedUsers(ctx, p.Users, p.GemNum); err != nil {
		return fmt.Errorf("seed users: %w", err)
	}
	if err := s.seedGuildMembers(ctx, p.Users, p.Guilds); err != nil {
		return fmt.Errorf("seed guild_members: %w", err)
	}
	// 個人ポイントの投入と同時にギルド別の合計を積み上げ、その値をそのまま
	// guild_scores へ投入する。乱数列を2度生成して突き合わせる作りにすると、
	// 片方の抽選順を変えた瞬間に MySQL 内部で不整合が生まれる（エラーも出ない）。
	guildTotals, err := s.seedUserPoints(ctx, p.Users, p.Guilds, rng)
	if err != nil {
		return fmt.Errorf("seed user_points: %w", err)
	}
	if err := s.seedGuildScores(ctx, guildTotals); err != nil {
		return fmt.Errorf("seed guild_scores: %w", err)
	}

	s.logger.InfoContext(ctx, "seeding completed",
		slog.Int("items", len(itemCatalog())),
		slog.Int("users", p.Users),
		slog.Int("guilds", p.Guilds),
	)
	return nil
}

func (s *Seeder) seedItems(ctx context.Context) error {
	catalog := itemCatalog()
	rows := make([][]any, 0, len(catalog))
	for i, it := range catalog {
		rows = append(rows, []any{int64(i + 1), it.name, it.rarity, it.weight})
	}
	return s.bulkUpsert(ctx, "items",
		[]string{"id", "name", "rarity", "weight"},
		"name = VALUES(name), rarity = VALUES(rarity), weight = VALUES(weight)",
		rows)
}

func (s *Seeder) seedGuilds(ctx context.Context, guilds int) error {
	rows := make([][]any, 0, guilds)
	for id := 1; id <= guilds; id++ {
		rows = append(rows, []any{int64(id), fmt.Sprintf("guild_%05d", id)})
	}
	return s.bulkUpsert(ctx, "guilds",
		[]string{"id", "name"},
		"name = VALUES(name)",
		rows)
}

func (s *Seeder) seedUsers(ctx context.Context, users, gemNum int) error {
	return s.chunked(ctx, users, func(from, to int) error {
		rows := make([][]any, 0, to-from+1)
		for id := from; id <= to; id++ {
			rows = append(rows, []any{int64(id), fmt.Sprintf("user_%08d", id), gemNum})
		}
		return s.bulkUpsert(ctx, "users",
			[]string{"id", "name", "gem_num"},
			"name = VALUES(name), gem_num = VALUES(gem_num)",
			rows)
	})
}

// seedGuildMembers は各ユーザーを 1 ギルドに割り当てる（user id を guilds 数で剰余分配）。
func (s *Seeder) seedGuildMembers(ctx context.Context, users, guilds int) error {
	return s.chunked(ctx, users, func(from, to int) error {
		rows := make([][]any, 0, to-from+1)
		for id := from; id <= to; id++ {
			rows = append(rows, []any{int64(guildIDForUser(id, guilds)), int64(id)})
		}
		return s.bulkUpsert(ctx, "guild_members",
			[]string{"guild_id", "user_id"},
			"guild_id = VALUES(guild_id)",
			rows)
	})
}

// seedUserPoints は個人ポイントを投入し、あわせてギルド別の合計を返す。
// 戻り値は guild_id をインデックスとする合計値（0 番は未使用）。
func (s *Seeder) seedUserPoints(ctx context.Context, users, guilds int, rng *rand.Rand) ([]int64, error) {
	guildTotals := make([]int64, guilds+1)
	err := s.chunked(ctx, users, func(from, to int) error {
		rows := make([][]any, 0, to-from+1)
		for id := from; id <= to; id++ {
			pts := int64(rng.IntN(maxInitialUserPoints + 1))
			guildTotals[guildIDForUser(id, guilds)] += pts
			rows = append(rows, []any{int64(id), pts})
		}
		return s.bulkUpsert(ctx, "user_points",
			[]string{"user_id", "points"},
			"points = VALUES(points)",
			rows)
	})
	if err != nil {
		return nil, err
	}
	return guildTotals, nil
}

// seedGuildScores は seedUserPoints が積み上げたギルド別合計をそのまま投入する。
// guildTotals は guild_id をインデックスとする合計値（0 番は未使用）。
func (s *Seeder) seedGuildScores(ctx context.Context, guildTotals []int64) error {
	rows := make([][]any, 0, len(guildTotals))
	for gid := 1; gid < len(guildTotals); gid++ {
		rows = append(rows, []any{int64(gid), guildTotals[gid]})
	}
	return s.bulkUpsert(ctx, "guild_scores",
		[]string{"guild_id", "score"},
		"score = VALUES(score)",
		rows)
}

// chunked は [1, total] を insertChunkSize 単位で区切り fn を呼ぶ。
func (s *Seeder) chunked(ctx context.Context, total int, fn func(from, to int) error) error {
	for from := 1; from <= total; from += insertChunkSize {
		to := from + insertChunkSize - 1
		if to > total {
			to = total
		}
		if err := fn(from, to); err != nil {
			return err
		}
	}
	return nil
}

// bulkUpsert は rows を複数行 INSERT ... ON DUPLICATE KEY UPDATE で投入する。
// rows が insertChunkSize を超える場合は分割する。
func (s *Seeder) bulkUpsert(ctx context.Context, table string, cols []string, updateClause string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	for start := 0; start < len(rows); start += insertChunkSize {
		end := start + insertChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]

		placeholderRow := "(" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
		valuesClause := strings.TrimSuffix(strings.Repeat(placeholderRow+",", len(chunk)), ",")
		// SQL を文字列組み立てしているが、table / cols / updateClause は本パッケージ内の
		// 固定値のみで、外部入力は一切混ざらない（値は必ずプレースホルダで渡す）。
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s ON DUPLICATE KEY UPDATE %s",
			table, strings.Join(cols, ", "), valuesClause, updateClause)

		args := make([]any, 0, len(chunk)*len(cols))
		for _, r := range chunk {
			args = append(args, r...)
		}
		if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("bulk upsert into %s: %w", table, err)
		}
	}
	return nil
}

// guildIDForUser はユーザーの所属ギルドを決める規則。
// guild_members の割り当てと guild_scores の集計で同じ規則を使う必要があるため、
// 各所に剰余計算を散らさず1箇所に集約する。
func guildIDForUser(userID, guilds int) int {
	return (userID-1)%guilds + 1
}

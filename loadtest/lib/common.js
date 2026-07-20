// loadtest/lib/common.js
// 各シナリオで共有する設定・ヘルパ。
//
// 環境変数（make/loadtest.mk から注入）:
//   BASE_URL  対象APIのベースURL（既定 http://localhost:8080）
//   USERS     seed で投入したユーザー数（ID空間 1..USERS）
//   GUILDS    seed で投入したギルド数（ID空間 1..GUILDS）
// 負荷形状の上書き:
//   RATE       維持する目標RPS
//   START_RATE ramp 開始RPS
//   RAMP       ramp-up 時間（例 30s）
//   DURATION   維持時間（例 1m）
//   PRE_VUS    事前確保VU数
//   MAX_VUS    最大VU数
import { check } from 'k6';

export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
export const USERS = parseInt(__ENV.USERS || '10000', 10);
export const GUILDS = parseInt(__ENV.GUILDS || '100', 10);

export const jsonParams = { headers: { 'Content-Type': 'application/json' } };

// seed 済みID空間からランダムに1件選ぶ（1-origin）。
export function randUserID() {
  return Math.floor(Math.random() * USERS) + 1;
}
export function randGuildID() {
  return Math.floor(Math.random() * GUILDS) + 1;
}

// 共通の合否基準。参照系/更新系で使い回す。
export const defaultThresholds = {
  http_req_failed: ['rate<0.01'], // エラー率 1% 未満
  http_req_duration: ['p(95)<200', 'p(99)<500'], // p95<200ms, p99<500ms
};

// RPS駆動（ramping-arrival-rate）シナリオを環境変数で組み立てる。
// VU駆動と違い、サーバが遅くなっても目標RPSを維持しようとするため実負荷計測に向く。
export function arrivalScenario(defaults = {}) {
  const rate = Number(__ENV.RATE || defaults.rate || 500);
  const startRate = Number(__ENV.START_RATE || defaults.startRate || 50);
  const ramp = __ENV.RAMP || defaults.ramp || '30s';
  const duration = __ENV.DURATION || defaults.duration || '1m';
  const preVUs = Number(__ENV.PRE_VUS || defaults.preVUs || 200);
  const maxVUs = Number(__ENV.MAX_VUS || defaults.maxVUs || 1000);

  return {
    main: {
      executor: 'ramping-arrival-rate',
      startRate,
      timeUnit: '1s',
      preAllocatedVUs: preVUs,
      maxVUs,
      stages: [
        { target: rate, duration: ramp },
        { target: rate, duration: duration },
        { target: 0, duration: '10s' },
      ],
    },
  };
}

// レスポンスのステータス検証。name はメトリクスタグ兼チェック名。
export function expectStatus(res, name, status = 200) {
  return check(res, {
    [`${name}: status ${status}`]: (r) => r.status === status,
  });
}

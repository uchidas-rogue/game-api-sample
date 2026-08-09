// loadtest/points.js
// スコア加算（書き込み系）シナリオ。個人ポイント加算＋ギルド集計＋outbox記録の複合パス。
// 同一ギルドへの集中書き込みでロック競合が起きやすい。CQRSの書き込み側の主戦場。
//
//   make load/points
//   RATE=2000 DURATION=3m make load/points
import http from 'k6/http';
import { arrivalScenario, defaultThresholds, BASE_URL, jsonParams, randUserID } from './lib/common.js';

// 加算ポイントの上限（1..この値の一様乱数）。
const MAX_POINTS = Number(__ENV.MAX_POINTS || 500);

export const options = {
  scenarios: arrivalScenario({ rate: 500 }),
  thresholds: defaultThresholds,
};

export default function () {
  const uid = randUserID();
  const points = Math.floor(Math.random() * MAX_POINTS) + 1;
  http.post(
    `${BASE_URL}/users/${uid}/points`,
    JSON.stringify({ points, reason: 'loadtest' }),
    Object.assign({ tags: { name: 'add_points' } }, jsonParams),
  );
}

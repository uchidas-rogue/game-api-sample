// loadtest/gacha.js
// ガチャ（書き込み系）シナリオ。トランザクション＋行ロックの負荷を見る。
// Aurora のデッドロック / ロック待ち / CPU を CloudWatch と突き合わせる対象。
//
//   make load/gacha
//   RATE=2000 DURATION=3m make load/gacha
import http from 'k6/http';
import { arrivalScenario, defaultThresholds, BASE_URL, jsonParams, randUserID } from './lib/common.js';

export const options = {
  scenarios: arrivalScenario({ rate: 500 }),
  thresholds: defaultThresholds,
};

export default function () {
  const uid = randUserID();
  // pull_count は 1..10 の範囲（範囲外は400）。ここは10連中心＋単発を混在させる。
  const pull = Math.random() < 0.8 ? 10 : Math.floor(Math.random() * 10) + 1;
  http.post(
    `${BASE_URL}/users/${uid}/gacha/multi`,
    JSON.stringify({ pull_count: pull }),
    Object.assign({ tags: { name: 'gacha_multi' } }, jsonParams),
  );
}

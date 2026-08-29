// ポートフォリオサイトのチャット中継。Cloudflare Workers 上で動く。
//
// 【なぜプロキシを挟むか】
// Anthropic の API キーをブラウザに置けないため。訪問者にキーを求める形にすると誰も使えないので、
// こちら側のキーで代理呼び出しする。その代わり、誰でも叩ける口になるので下の防御を入れている。
//
// 【なぜ本文をクライアントから受け取らないか】
// リクエストで受け取るのは質問文とチャンク ID だけ。本文は Worker がバンドル済みの索引
// （web/data/index.json）から解決する。任意テキストを送り込んで無料の LLM として使われる経路を
// 塞ぐためで、これが本ファイルの一番重要な設計判断。
//
// 【AWS ではなく Workers に置く理由】
// このリポジトリの AWS 環境はコスト削減のため destroy する運用を前提にしている。
// チャットを AWS 側に置くと、環境を落とした瞬間にポートフォリオのチャットも死ぬ。

import Anthropic from "@anthropic-ai/sdk";
import indexData from "../../data/index.json";

/** 索引のチャンク（scripts/sitegen の出力と対応。片方だけ変えないこと）。 */
type Chunk = {
  id: string;
  file: string;
  anchor: string;
  title: string;
  trail: string;
  text: string;
};

/** 上限値。ブラウザと共有するため索引に載っている（正本は scripts/sitegen の const）。 */
type Limits = { topK: number; maxQuestionChars: number };

type Index = { repo: string; branch: string; limits: Limits; chunks: Chunk[] };

type ChatRequest = { question?: unknown; chunkIds?: unknown };

export type Env = {
  /** Anthropic の API キー。`wrangler secret put ANTHROPIC_API_KEY` で設定する。 */
  ANTHROPIC_API_KEY: string;
  /** レート制限・日次上限のカウンタ置き場。 */
  CHAT_KV: KVNamespace;
  /** 許可するオリジン（カンマ区切り）。未設定ならローカル開発のみ許可。 */
  ALLOWED_ORIGINS?: string;
};

/** モデル。抜粋を渡して要約・説明させる用途なので Haiku で足りる（コストは README.md の表を参照）。 */
const MODEL = "claude-haiku-4-5";
/** 1回の回答の上限。長い回答は読まれないうえ、そのままコストになる。 */
const MAX_TOKENS = 1024;
/** 1 IP あたりの毎分リクエスト上限。 */
const RATE_PER_MINUTE = 10;
/** サイト全体の1日あたり呼び出し上限（想定コストの天井を決める値）。 */
const DAILY_CAP = 200;
/**
 * 1 IP あたりの1日の呼び出し上限。
 *
 * サイト全体の上限だけだと、毎分上限いっぱいで叩き続ける1人が 20 分でその日の枠を
 * 使い切れてしまい、他の訪問者がチャットを使えなくなる。全体の枠を食い潰すのに
 * 最低 7 IP 必要になるところまで下げる。
 */
const DAILY_CAP_PER_IP = 30;
/** 毎分カウンタの保持期間（秒）。次の窓に入れば読まれないので短くてよい。 */
const MINUTE_TTL = 120;
/** 日次カウンタの保持期間（秒）。日付が変わるまで持てばよい。 */
const DAY_TTL = 172_800;

// JSON インポートの推論型（数百件のオブジェクトリテラル）をそのまま扱うと型チェックが重いので、
// 生成物の形を Index として宣言し直す。形の固定は scripts/sitegen のテストが担当する。
const index = indexData as unknown as Index;
const chunkById = new Map(index.chunks.map((chunk) => [chunk.id, chunk]));

// 質問文とチャンク数の上限は索引から読む。同じ索引をブラウザも読むので、
// 2つのランタイムへ同じ数字を書き写す必要がなくなる（正本は scripts/sitegen の const）。
const { topK: MAX_CHUNKS, maxQuestionChars: MAX_QUESTION_CHARS } = index.limits;

const SYSTEM_PROMPT = [
  "あなたは GitHub リポジトリ uchidas-rogue/game-api-sample の内容を説明するアシスタントです。",
  "回答は、ユーザーメッセージに含まれる「抜粋」だけを根拠にしてください。",
  "",
  "規則:",
  "- 抜粋に書かれていないことは推測せず、「リポジトリの文書には見当たりません」と答える",
  "- 日本語で、3〜6文程度の簡潔な説明にする。箇条書きは要点が複数あるときだけ使う",
  "- 数値・設定値・コマンドは抜粋どおりに引用し、丸めたり言い換えたりしない",
  "- 設計の「理由」が抜粋にあるなら、結論より先に理由を述べる",
  "- 出典は画面側が表示するため、本文にリンクや脚注を書かない",
].join("\n");

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const origin = request.headers.get("Origin") ?? "";
    const allowed = allowedOrigins(env);
    const cors = corsHeaders(origin, allowed);

    if (request.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: cors });
    }
    if (request.method !== "POST") {
      return problem(405, "POST のみ受け付けます。", cors);
    }
    if (origin !== "" && !allowed.includes(origin)) {
      return problem(403, "このオリジンからの利用は許可されていません。", cors);
    }

    let body: ChatRequest;
    try {
      body = (await request.json()) as ChatRequest;
    } catch {
      return problem(400, "リクエストの形式が不正です。", cors);
    }

    const question = typeof body.question === "string" ? body.question.trim() : "";
    if (question === "" || question.length > MAX_QUESTION_CHARS) {
      return problem(400, `質問は 1〜${MAX_QUESTION_CHARS} 文字で送ってください。`, cors);
    }

    const ids = Array.isArray(body.chunkIds) ? body.chunkIds.slice(0, MAX_CHUNKS) : [];
    const chunks = ids
      .filter((id): id is string => typeof id === "string")
      .map((id) => chunkById.get(id))
      .filter((chunk): chunk is Chunk => chunk !== undefined);
    if (chunks.length === 0) {
      // 実際にこれが返るのは、サイトの索引を更新したあと Worker を再デプロイしていないとき
      // （ブラウザが送る ID と、ここにバンドルされた索引の ID が食い違う）。
      // デプロイが手動なので、原因が推測できる文面にしておく。
      return problem(400, "参照する文書を特定できませんでした（索引が更新された可能性があります）。", cors);
    }

    const limited = await checkLimits(env, request);
    if (limited !== null) {
      return problem(limited.status, limited.message, cors);
    }

    return streamAnswer(env, question, chunks, cors);
  },
};

/** allowedOrigins は許可オリジンの一覧を返す。ローカル開発用は常に許可する。 */
function allowedOrigins(env: Env): string[] {
  const configured = (env.ALLOWED_ORIGINS ?? "").split(",").map((value) => value.trim()).filter(Boolean);
  return [...configured, "http://localhost:8000", "http://127.0.0.1:8000"];
}

function corsHeaders(origin: string, allowed: string[]): Record<string, string> {
  return {
    "access-control-allow-origin": allowed.includes(origin) ? origin : allowed[0] ?? "",
    "access-control-allow-headers": "content-type",
    "access-control-allow-methods": "POST, OPTIONS",
    "access-control-max-age": "86400",
  };
}

function problem(status: number, message: string, cors: Record<string, string>): Response {
  return new Response(JSON.stringify({ message }), {
    status,
    headers: { ...cors, "content-type": "application/json; charset=utf-8" },
  });
}

/**
 * checkLimits は IP 単位のレート制限と日次上限を見る。
 * 制限に掛かったら status と文面を返し、通れば null を返す。
 *
 * 【全部 get して判定してから put する】
 * 順に「get → 判定 → put」を繰り返すと、後段の上限で弾かれるリクエストでも前段の
 * カウンタには書き込んでしまう。以前は毎分カウンタの put が日次上限の判定より前にあり、
 * 枠を使い切った後も 1 IP あたり最大 14,400 write/day（10 req/min × 60 × 24）を
 * 発生させられた。KV の無料枠は 1,000 write/day なので、そこで書き込みが枯れると
 * カウンタが更新されなくなり、レート制限そのものが黙って効かなくなる。
 * 通ったリクエストしか書かない形にすれば、書き込みは DAILY_CAP × カウンタ数
 * （200 × 3 = 600 write/day）で頭打ちになる。
 *
 * KV は結果整合なので厳密なカウントにはならない。ここで守りたいのは「誰か1人の連打で
 * その日のコストを使い切られない」ことなので、取りこぼしを許容して単純な実装にしている。
 */
async function checkLimits(env: Env, request: Request): Promise<{ status: number; message: string } | null> {
  const ip = request.headers.get("CF-Connecting-IP") ?? "unknown";
  const day = new Date().toISOString().slice(0, 10);
  const minute = Math.floor(Date.now() / 60_000);

  const counters = [
    {
      key: `day:${ip}:${day}`,
      limit: DAILY_CAP_PER_IP,
      ttl: DAY_TTL,
      message: `本日の利用上限に達しました（1人あたり1日 ${DAILY_CAP_PER_IP} 回）。リポジトリの文書は GitHub から直接読めます。`,
    },
    {
      key: `day:${day}`,
      limit: DAILY_CAP,
      ttl: DAY_TTL,
      message: "本日の利用上限に達しました。リポジトリの文書は GitHub から直接読めます。",
    },
    {
      key: `rate:${ip}:${minute}`,
      limit: RATE_PER_MINUTE,
      ttl: MINUTE_TTL,
      message: "リクエストが多すぎます。1分ほど待ってから試してください。",
    },
  ];

  const next = await Promise.all(
    counters.map(async (counter) => Number((await env.CHAT_KV.get(counter.key)) ?? "0") + 1),
  );

  const exceeded = counters.findIndex((counter, i) => next[i] > counter.limit);
  if (exceeded !== -1) {
    return { status: 429, message: counters[exceeded].message };
  }

  await Promise.all(
    counters.map((counter, i) => env.CHAT_KV.put(counter.key, String(next[i]), { expirationTtl: counter.ttl })),
  );
  return null;
}

/** streamAnswer はモデルの応答を SSE（data: {json}）でそのまま流す。 */
function streamAnswer(env: Env, question: string, chunks: Chunk[], cors: Record<string, string>): Response {
  const client = new Anthropic({ apiKey: env.ANTHROPIC_API_KEY });
  const encoder = new TextEncoder();

  const body = new ReadableStream({
    async start(controller) {
      const send = (payload: unknown) => {
        controller.enqueue(encoder.encode(`data: ${JSON.stringify(payload)}\n\n`));
      };

      try {
        const stream = client.messages.stream({
          model: MODEL,
          max_tokens: MAX_TOKENS,
          system: SYSTEM_PROMPT,
          messages: [{ role: "user", content: buildUserMessage(question, chunks) }],
        });

        for await (const event of stream) {
          if (event.type === "content_block_delta" && event.delta.type === "text_delta") {
            send({ type: "delta", text: event.delta.text });
          }
        }

        const message = await stream.finalMessage();
        if (message.stop_reason === "refusal") {
          send({ type: "error", message: "この質問には回答できませんでした。" });
        }
      } catch (error) {
        console.error("chat failed", error);
        send({ type: "error", message: "回答の生成に失敗しました。時間をおいて試してください。" });
      } finally {
        controller.close();
      }
    },
  });

  return new Response(body, {
    headers: {
      ...cors,
      "content-type": "text/event-stream; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

/** buildUserMessage は抜粋と質問を1つのユーザーメッセージへ組み立てる。 */
function buildUserMessage(question: string, chunks: Chunk[]): string {
  const excerpts = chunks
    .map((chunk, i) => `## 抜粋${i + 1}: ${chunk.trail}\n${chunk.text}`)
    .join("\n\n");

  return `# リポジトリの文書からの抜粋\n\n${excerpts}\n\n# 質問\n\n${question}`;
}

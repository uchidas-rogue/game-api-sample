// web/app.js のブラウザ側の挙動を jsdom 上で検証する。
//
// なぜ必要か: app.js は index.json の読み込み・BM25 検索・SSE の読み取り・フォールバックまでを
// 担うのに、実行環境がブラウザなので Go のテストからは触れない。ここが無いと
// 「レビューと目視でしか担保されていないコード」がリポジトリに1つだけ残る。
//
// とくに「送信内容が question と chunkIds だけであること」（§3）は、
// 無料の LLM として使われないという設計の主張そのもの。ここが壊れたら設計の前提が崩れる。
import { test, describe, before } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { JSDOM } from "jsdom";

const WEB = new URL("../", import.meta.url);
const read = (name) => fs.readFileSync(new URL(name, WEB), "utf8");

// <script src="app.js"> は jsdom がフェッチできないので外し、代わりに下で直接流し込む。
const html = read("index.html").replace(/<script src="app\.js"><\/script>/, "");
const appjs = read("app.js");
const indexJson = JSON.parse(read("data/index.json"));

/** 検索が必ず当たる質問（索引の実データに対して閾値を超えるもの）。 */
const HIT_QUESTION = "outbox のスループットはどう改善した？";

/**
 * boot はサイトを jsdom 上で起動する。
 * chat は中継への fetch が返す応答を組み立てる関数。sent には送信されたボディが溜まる。
 */
function boot({ endpoint = "", chat } = {}) {
  const dom = new JSDOM(
    html.replace('name="chat-endpoint" content=""', `name="chat-endpoint" content="${endpoint}"`),
    { runScripts: "dangerously", url: "http://localhost:8000/" },
  );
  const { window } = dom;
  // jsdom は未実装。app.js が新着メッセージへスクロールするだけなので握り潰す。
  window.Element.prototype.scrollIntoView = () => {};

  const sent = [];
  window.fetch = async (url, options) => {
    if (String(url).includes("index.json")) {
      return { ok: true, json: async () => indexJson };
    }
    sent.push(JSON.parse(options.body));
    return chat();
  };

  const script = window.document.createElement("script");
  script.textContent = appjs;
  window.document.body.appendChild(script);
  return { window, sent };
}

/** settle は app.js の非同期処理（索引の読み込み・fetch・ストリーム読み取り）が落ち着くまで待つ。 */
const settle = () => new Promise((resolve) => setTimeout(resolve, 80));

/** ask は質問を送信し、処理が終わるまで待つ。 */
async function ask(window, question) {
  window.document.getElementById("question").value = question;
  window.document
    .getElementById("composer")
    .dispatchEvent(new window.Event("submit", { cancelable: true, bubbles: true }));
  await settle();
}

/** answer は最後に表示された吹き出しを返す。 */
const answer = (window) => [...window.document.querySelectorAll("#log .msg")].pop();
const count = (window, selector) => window.document.querySelectorAll(selector).length;

/** stubbedError は中継が status とメッセージを返す状況を作る。 */
const stubbedError = (status, message) => async () => ({
  ok: false,
  status,
  json: async () => ({ message }),
});

describe("索引の読み込み", () => {
  test("読み込むまで送信できず、読み込めたら上限が索引から反映される", async () => {
    const { window } = boot();
    const send = window.document.getElementById("send");

    assert.equal(send.disabled, true, "索引が無い状態で検索させない");

    await settle();

    assert.equal(send.disabled, false);
    assert.equal(
      window.document.getElementById("question").maxLength,
      indexJson.limits.maxQuestionChars,
      "上限は索引の limits が正本。html に数字を直接書かない",
    );
    assert.match(window.document.getElementById("status").textContent, /個の節を検索できます/);
  });

  test("読み込みの途中で送信されても落ちない", async () => {
    // 送信ボタンは無効にしてあるが、入力欄で Enter を押す経路が残る。
    // 索引がまだ無い状態で検索させると limits が undefined のまま参照されて例外になる。
    const { window, sent } = boot({
      endpoint: "http://proxy.test/chat",
      chat: async () => assert.fail("索引が無い状態で中継を呼んではいけない"),
    });

    await ask(window, HIT_QUESTION); // settle() を挟まず、読み込み前に送る

    assert.equal(sent.length, 0);
  });
});

describe("中継へ送る内容", () => {
  let sentBody;

  before(async () => {
    const { window, sent } = boot({
      endpoint: "http://proxy.test/chat",
      chat: stubbedError(400, "参照する文書を特定できませんでした（索引が更新された可能性があります）。"),
    });
    await settle();
    await ask(window, HIT_QUESTION);
    sentBody = sent[0];
  });

  test("送るのは question と chunkIds だけ", () => {
    // 本文を送れる形にすると、任意テキストを流し込んで無料の LLM として使われる。
    assert.deepEqual(Object.keys(sentBody).sort(), ["chunkIds", "question"]);
  });

  test("chunkIds は文字列 ID のみで、上限を超えない", () => {
    assert.ok(sentBody.chunkIds.length > 0);
    assert.ok(sentBody.chunkIds.length <= indexJson.limits.topK);
    for (const id of sentBody.chunkIds) {
      assert.equal(typeof id, "string");
      assert.ok(indexJson.chunks.some((chunk) => chunk.id === id), `${id} は索引に存在すること`);
    }
  });
});

describe("回答が得られないときのフォールバック", () => {
  test("中継先が未設定なら、中継を呼ばずに抜粋を出す", async () => {
    const { window, sent } = boot({ endpoint: "" });
    await settle();

    await ask(window, HIT_QUESTION);

    assert.equal(sent.length, 0, "設定されていない相手を呼ばない");
    assert.match(answer(window).textContent, /抜粋を表示します/);
    assert.ok(count(window, "#log .excerpt") > 0);
    assert.ok(count(window, "#log .sources a") > 0, "出典リンクは常に添える");
  });

  test("索引がズレて 400 が返っても、理由を残したまま抜粋へ落ちる", async () => {
    // サイトの索引を更新して Worker を再デプロイし忘れると、この経路に入る。
    // デプロイが手動である以上、この表示が運用側の唯一の気づく手がかりになる。
    const { window } = boot({
      endpoint: "http://proxy.test/chat",
      chat: stubbedError(400, "参照する文書を特定できませんでした（索引が更新された可能性があります）。"),
    });
    await settle();

    await ask(window, HIT_QUESTION);

    const text = answer(window).textContent;
    assert.match(text, /索引が更新された可能性があります/, "黙って劣化させない");
    assert.match(text, /抜粋を表示します/);
    assert.ok(count(window, "#log .excerpt") > 0);
  });

  test("レート制限（429）でも理由が残る", async () => {
    const { window } = boot({
      endpoint: "http://proxy.test/chat",
      chat: stubbedError(429, "リクエストが多すぎます。1分ほど待ってから試してください。"),
    });
    await settle();

    await ask(window, HIT_QUESTION);

    assert.match(answer(window).textContent, /リクエストが多すぎます/);
    assert.ok(count(window, "#log .excerpt") > 0);
  });

  test("中継へ到達できなくても抜粋へ落ちる", async () => {
    const { window } = boot({
      endpoint: "http://proxy.test/chat",
      chat: async () => {
        throw new Error("network down");
      },
    });
    await settle();

    await ask(window, HIT_QUESTION);

    assert.match(answer(window).textContent, /接続できませんでした/);
    assert.ok(count(window, "#log .excerpt") > 0);
  });
});

describe("正常系", () => {
  test("SSE のデルタを連結して表示し、出典を添える", async () => {
    const events = [
      'data: {"type":"delta","text":"バッチ tx へ"}\n\n',
      'data: {"type":"delta","text":"変えました。"}\n\n',
    ];
    const { window } = boot({
      endpoint: "http://proxy.test/chat",
      chat: async () => ({
        ok: true,
        body: {
          getReader() {
            let i = 0;
            return {
              read: async () =>
                i < events.length
                  ? { value: new TextEncoder().encode(events[i++]), done: false }
                  : { done: true },
            };
          },
        },
      }),
    });
    await settle();

    await ask(window, HIT_QUESTION);

    assert.match(answer(window).textContent, /バッチ tx へ変えました。/);
    assert.ok(count(window, "#log .sources a") > 0);
  });
});

describe("検索で落とす", () => {
  test("どの語も索引に無い質問では中継を呼ばない", async () => {
    // スコアが 0 になる経路。中継を呼ぶと、答えられないうえにコストだけ掛かる。
    const { window, sent } = boot({
      endpoint: "http://proxy.test/chat",
      chat: async () => assert.fail("呼んではいけない"),
    });
    await settle();

    await ask(window, "今日の天気は？");

    assert.equal(sent.length, 0);
    assert.match(answer(window).textContent, /見つかりませんでした/);
  });

  test("語は当たるがスコアが低い質問も中継を呼ばない", async () => {
    // 上のケースと別の経路。助詞や挨拶は bigram がどこかに当たるのでスコアが 0 にならず、
    // MIN_SCORE の足切りだけが中継を止めている。ここが無いと閾値を外しても気づけない。
    //
    // このケースだけは索引の中身に依存する（執筆時点で「おはよう」は 3.4 前後 = 閾値 6 の 4 割下）。
    // 索引が育って閾値を超えたらこのテストが落ちるが、それは app.js の
    // 「索引が大きく変わったら見直す」というコメントどおり MIN_SCORE を見直す合図なので、
    // テストを緩めるのではなく閾値の側を再検討すること。
    const { window, sent } = boot({
      endpoint: "http://proxy.test/chat",
      chat: async () => assert.fail("呼んではいけない"),
    });
    await settle();

    await ask(window, "おはよう");

    assert.equal(sent.length, 0);
    assert.match(answer(window).textContent, /見つかりませんでした/);
  });

  test("上限を超える質問は送信前に弾く", async () => {
    const { window, sent } = boot({
      endpoint: "http://proxy.test/chat",
      chat: async () => assert.fail("呼んではいけない"),
    });
    await settle();

    await ask(window, "あ".repeat(indexJson.limits.maxQuestionChars + 1));

    assert.equal(sent.length, 0);
    assert.match(answer(window).textContent, /文字までにしてください/);
  });
});

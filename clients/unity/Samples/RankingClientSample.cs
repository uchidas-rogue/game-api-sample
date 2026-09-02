// RankingClientSample.cs
//
// game-api-sample の RankingService を Unity から呼ぶ最小サンプル。
// unary（GetUserRankings）と server streaming（WatchUserRankings）の両方を1枚で示す。
//
// 前提:
//   - gRPC サーバが起動していること（リポジトリルートで `make run/grpc`。既定 :9090、平文 h2c）
//   - Grpc.Net.Client / Google.Protobuf / Grpc.Core.Api / YetAnotherHttpHandler の導入が済んでいること
//   - link.xml が Assets/ 配下にあること（IL2CPP ビルド時に必須。理由は link.xml のコメント参照）
// 導入手順は clients/unity/README.md を参照。
//
// 使い方: 空の GameObject にアタッチして再生する。

using System;
using System.Threading;
using System.Threading.Tasks;
using Cysharp.Net.Http;
using Grpc.Core;
using Grpc.Net.Client;
using UnityEngine;

namespace Game.Ranking.V1
{
    /// <summary>
    /// RankingService への接続・unary 呼び出し・streaming 購読を1つの MonoBehaviour で行うサンプル。
    /// </summary>
    public sealed class RankingClientSample : MonoBehaviour
    {
        [Header("接続先")]
        [Tooltip("gRPC サーバのアドレス。平文 h2c なので http:// で指定する（https ではない）。")]
        [SerializeField] private string address = "http://localhost:9090";

        [Header("取得件数")]
        [SerializeField] private int limit = 10;

        // Unity の HttpClient は HTTP/2 を話せないため、gRPC には使えない。
        // YetAnotherHttpHandler が HTTP/2 を喋る HttpMessageHandler を提供する（README §なぜこの構成か）。
        private YetAnotherHttpHandler _handler;
        private GrpcChannel _channel;
        private RankingService.RankingServiceClient _client;

        // 再生停止・シーン破棄でストリームと接続を確実に畳むためのトークン。
        // これが無いと OnDestroy 後も購読ループが生き残り、エディタが固まる/接続が残る。
        private CancellationTokenSource _cts;

        private void Start()
        {
            _cts = new CancellationTokenSource();

            // Http2Only = true が要点。平文 h2c では ALPN による HTTP/2 ネゴシエーションができないので、
            // 明示しないと HTTP/1.1 で接続を張ってしまい gRPC が成立しない。
            _handler = new YetAnotherHttpHandler { Http2Only = true };

            _channel = GrpcChannel.ForAddress(address, new GrpcChannelOptions
            {
                HttpHandler = _handler,

                // 既定は false（= 呼び出し側が handler を破棄する責任を持つ）。
                // true にして channel.Dispose() に handler の破棄まで任せる。
                DisposeHttpClient = true,
            });

            _client = new RankingService.RankingServiceClient(_channel);

            // async void を避けるため Task を投げっぱなしにするが、
            // 中で全例外を捕まえているので未観測例外にはならない。
            _ = RunAsync(_cts.Token);
        }

        private async Task RunAsync(CancellationToken token)
        {
            try
            {
                await GetUserRankingsOnceAsync(token);
                await WatchUserRankingsAsync(token);
            }
            catch (OperationCanceledException)
            {
                // OnDestroy による正常な打ち切り。エラーではない。
                Debug.Log("[Ranking] 購読を終了しました。");
            }
            catch (RpcException e)
            {
                LogRpcException(e);
            }
        }

        /// <summary>unary の例。GetUserRankings を1回だけ呼んで結果をログ出力する。</summary>
        private async Task GetUserRankingsOnceAsync(CancellationToken token)
        {
            // unary には deadline を必ず入れる。入れないとサーバが無応答のとき永久に待つ。
            // （streaming 側には入れない。購読を打ち切ってしまうため。)
            var res = await _client.GetUserRankingsAsync(
                new GetUserRankingsRequest { Limit = limit, Offset = 0 },
                deadline: DateTime.UtcNow.AddSeconds(5),
                cancellationToken: token);

            Debug.Log($"[Ranking] GetUserRankings: total={res.TotalCount} 件中 {res.Rankings.Count} 件取得");
            foreach (var entry in res.Rankings)
            {
                Debug.Log($"[Ranking]   #{entry.Rank} {entry.Name}(id={entry.Id}) score={entry.Score}");
            }
        }

        /// <summary>
        /// server streaming の例。WatchUserRankings を購読し、更新が来るたびにログ出力する。
        /// 購読直後に現在のスナップショットが1件届き、以降はランキングが変化したときだけ届く。
        /// </summary>
        private async Task WatchUserRankingsAsync(CancellationToken token)
        {
            // using で AsyncServerStreamingCall を破棄する。これがストリームの解放にあたる。
            using var call = _client.WatchUserRankings(
                new WatchUserRankingsRequest { Limit = limit },
                cancellationToken: token);

            Debug.Log("[Ranking] WatchUserRankings の購読を開始しました。");

            // await foreach は C# 8 以降。Unity の Api Compatibility Level を .NET Standard 2.1 にすること。
            // continuation は Unity の SynchronizationContext に戻るため、この中で Unity API を触ってよい。
            await foreach (var res in call.ResponseStream.ReadAllAsync(token))
            {
                Debug.Log($"[Ranking] push 受信: total={res.TotalCount} 件中 {res.Rankings.Count} 件");
                foreach (var entry in res.Rankings)
                {
                    Debug.Log($"[Ranking]   #{entry.Rank} {entry.Name}(id={entry.Id}) score={entry.Score}");
                }
            }
        }

        private static void LogRpcException(RpcException e)
        {
            // ランキングが Redis に構築されていないと Unavailable が返る（HTTP 版の 503 に対応）。
            // サーバ側で `make load/warm` を実行して再構築する。
            if (e.StatusCode == StatusCode.Unavailable)
            {
                Debug.LogError($"[Ranking] サーバへ到達できないか、ランキングが未構築です: {e.Status.Detail}");
                return;
            }

            Debug.LogError($"[Ranking] RPC 失敗 code={e.StatusCode} detail={e.Status.Detail}");
        }

        /// <summary>
        /// ストリームと接続を明示的に畳む。
        ///
        /// **ここを書かないと、エディタの再生を停止してもストリームの購読ループと TCP 接続が residual に残る。**
        /// Unity はドメインリロードを跨いでネイティブ側の接続を回収してくれないため、
        /// 再生のたびに接続が積み上がり、最終的にエディタが固まる。
        /// </summary>
        private void OnDestroy()
        {
            // 1. 進行中の RPC と await foreach ループへ打ち切りを伝える
            _cts?.Cancel();

            // 2. チャネル（DisposeHttpClient = true なので handler も一緒に破棄される）
            _channel?.Dispose();
            _channel = null;
            _handler = null;

            // 3. CTS 自身
            _cts?.Dispose();
            _cts = null;
        }
    }
}

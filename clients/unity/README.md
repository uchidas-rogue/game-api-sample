# clients/unity — Unity 向け gRPC クライアント

`proto/game/ranking/v1/ranking.proto` から生成した C# と、Unity から `RankingService` を
呼ぶための最小サンプル・IL2CPP 用の stripping 設定を置く。

**このディレクトリは Unity プロジェクトではない。** Unity プロジェクトの `Assets/` 配下へ
コピーして使う（手順は「導入手順」）。

| ファイル | 役割 |
|---|---|
| `Runtime/Generated/Ranking.cs` | メッセージ型。`make proto/gen/csharp` の生成物 |
| `Runtime/Generated/RankingGrpc.cs` | `RankingService.RankingServiceClient`。同上 |
| `Samples/RankingClientSample.cs` | unary と server streaming を1枚で示す MonoBehaviour |
| `link.xml` | IL2CPP の code stripping 対策。**必須** |
| `.proto-digest` | 生成物が `.proto` に追随しているかの判定用（`make proto/gen/check`） |

名前空間は `.proto` の `option csharp_namespace` に従って `Game.Ranking.V1`。

---

## なぜこの構成なのか（先に読むこと）

Unity で gRPC を動かす方法は歴史的に何度も変わっており、**検索で出てくる情報の大半が古い**。
今から新規に書くなら選択肢は実質ひとつしかない。その理由を先に押さえておくと、
以降の手順の意味が分かる。

### 1. `Grpc.Core`（C-core）は使ってはいけない

長らく Unity での定番だった `Grpc.Core`（ネイティブライブラリを P/Invoke する実装）は
**gRPC 公式が非推奨（deprecated）とし、すでにメンテナンスを終了してリポジトリもアーカイブ済み**。
セキュリティ修正も新しいプラットフォーム（Apple Silicon / 新しい Android ABI 等）への
対応も行われない。**新規に採用してはならない。**

> 非推奨アナウンスとサポート終了の正確な時期は
> [gRPC 公式ブログの C# Core-library deprecation の告知](https://grpc.io/blog/) を参照。
> Unity + gRPC の記事は `Grpc.Core` 前提のものが今も多数残っているので、
> **記事を見つけたら最初に「どちらの実装か」を確認する。**

### 2. 後継の grpc-dotnet は HTTP/2 を要求するが、Unity のランタイムは喋れない

後継は grpc-dotnet（`Grpc.Net.Client` の `GrpcChannel`）。これは C-core と違い
**`System.Net.Http.HttpClient` の上に載る純マネージド実装**で、gRPC の通信路として
**HTTP/2 を要求する**（gRPC のプロトコル仕様そのものが HTTP/2 前提）。

ところが **Unity の Mono / IL2CPP ランタイムに同梱される `HttpClient` は HTTP/2 に対応していない。**
そのため `Grpc.Net.Client` を入れただけでは動かない。素で `GrpcChannel.ForAddress()` すると、
HTTP/1.1 で接続を張ろうとして失敗する。

**ここが最大の詰まりどころ。** 「NuGet で `Grpc.Net.Client` を入れたのに動かない」の原因はほぼこれ。

### 3. 解: `YetAnotherHttpHandler` を `GrpcChannel` に挿す

現在の解は Cysharp の **[YetAnotherHttpHandler](https://github.com/Cysharp/YetAnotherHttpHandler)**。
Rust 実装の HTTP/2 スタックをネイティブプラグインとして持ち込み、
**Unity で HTTP/2 を喋れる `HttpMessageHandler`** を提供するライブラリ。
これを `GrpcChannelOptions.HttpHandler` に渡すと grpc-dotnet がそのまま動く。

```csharp
var handler = new YetAnotherHttpHandler { Http2Only = true };
var channel = GrpcChannel.ForAddress("http://localhost:9090", new GrpcChannelOptions
{
    HttpHandler = handler,
    DisposeHttpClient = true,
});
```

`Http2Only = true` は**平文 h2c（TLS 無しの HTTP/2）で必須**。
TLS があれば ALPN で HTTP/2 をネゴシエートできるが、平文にはその仕組みが無いため、
明示しないと HTTP/1.1 で繋ぎにいって gRPC が成立しない。本リポジトリのサーバは
平文 h2c なので、**この指定を外すと動かない**。

---

## 導入手順

### 1. Unity プロジェクトへコピー

`clients/unity/` の中身を、Unity プロジェクトの `Assets/` 配下の任意のフォルダへコピーする。

```
<UnityProject>/Assets/GameApi/
├── Runtime/Generated/Ranking.cs
├── Runtime/Generated/RankingGrpc.cs
├── Samples/RankingClientSample.cs
└── link.xml
```

`.proto-digest` はリポジトリ側の drift 検知用なのでコピー不要。

`link.xml` は `Assets/` 配下にあればどこでもよい（Unity が自動で拾う）。
**省略しないこと。** 理由は後述の「IL2CPP ビルドだけ落ちる」を参照。

### 2. 依存パッケージを入れる

**DLL をこのリポジトリにコミットしない方針**を取っている。
バイナリの出所とバージョンが git 履歴から追えなくなり、更新の判断もできなくなるため。
各自の Unity プロジェクト側でパッケージマネージャ経由で入れること。
（`Runtime/Generated/` の `.cs` だけは例外的にコミットしている。Unity が NuGet を
標準サポートせず、生成に buf + ネットワークが要るため。`proto/buf.gen.csharp.yaml` のコメント参照）

| 依存 | 入手経路 |
|---|---|
| `YetAnotherHttpHandler` | UPM（Package Manager の "Add package from git URL"） |
| `Grpc.Net.Client` | NuGetForUnity |
| `Grpc.Core.Api` | NuGetForUnity（`Grpc.Net.Client` の依存として入ることが多い） |
| `Google.Protobuf` | NuGetForUnity |

- **YetAnotherHttpHandler**: UPM の git URL 形式・要求 Unity バージョン・
  ネイティブプラグインの配置手順は更新されることがあるため、
  **[公式リポジトリの README の導入手順に従うこと](https://github.com/Cysharp/YetAnotherHttpHandler)**。
  ここに URL を書き写すと古くなる。
- **NuGetForUnity**: Unity は NuGet を標準サポートしないので、
  [NuGetForUnity](https://github.com/GlitchEnzo/NuGetForUnity) を入れてから上記3つを検索・追加する。
  導入手順は同リポジトリの README を参照。
- `Grpc.Tools` は**不要**。C# の生成はこのリポジトリ側で `make proto/gen/csharp` が行う。

### 3. Player Settings

| 設定 | 値 | 理由 |
|---|---|---|
| Api Compatibility Level | **.NET Standard 2.1** | サンプルの `await foreach` が使う `IAsyncEnumerable` に必要 |
| Managed Stripping Level | 任意（`link.xml` で保護済み） | — |

サンプルの `await foreach`（C# 8）を使うため、Unity のバージョンは C# 8 以降を
サポートするものが要る。正確な下限は YetAnotherHttpHandler が要求する Unity バージョンにも
依存するので、**公式の導入手順で確認すること**。

### 4. サーバを起動する

リポジトリルートで:

```bash
make run/grpc     # 既定 :9090
```

`GRPC_PORT` 環境変数でポートを変えられる（既定 9090。`configs/config.go`）。

**サーバは平文 h2c（TLS 無し）で listen する。** したがってクライアント側のアドレスは
`https://` ではなく **`http://localhost:9090`**。TLS 対応は未実装で ROADMAP 側の課題。
<!-- ssot-assert: absent-grep 'credentials' internal/infrastructure/server/grpc.go -->

ランキングを返すには Redis にランキングが構築されている必要がある。
未構築だと `codes.Unavailable`（HTTP 版の 503 に相当）が返るので、
`make load/seed` または `make load/warm` を実行しておく。

### 5. 動かす

空の GameObject に `RankingClientSample` をアタッチして再生する。
Console に `GetUserRankings` の結果と、以降 `WatchUserRankings` の push が出れば成功。

---

## 詰まりどころ

### エディタでは動くのに IL2CPP ビルドだけ落ちる → `link.xml` が無い

**最も厄介な失敗。** エディタ（Mono、stripping 無し）では再現しないため、
実機ビルドまで気づかない。

Google.Protobuf は生成コードに埋め込まれた descriptor を起動時にパースし、
そこに書かれた型名から C# 型を**リフレクションで**解決する。
IL2CPP のリンカはこの経路を追えず「未参照」と判断して型を削るため、
`TypeInitializationException` や `TypeLoadException` で落ちる。

`link.xml` を `Assets/` 配下に置けば解決する。
**`Runtime/` に `.asmdef` を切った場合は、そのアセンブリ名を `link.xml` に追加すること**
（既定では生成コードは `Assembly-CSharp` に入る前提で書いてある）。
漏れてもビルドは通り、実機で初めて分かる。

### 接続できない / タイムアウトする

- `Http2Only = true` を付け忘れていないか（平文 h2c では必須。上記「なぜこの構成なのか」§3）
- アドレスが `https://` になっていないか（サーバは平文なので `http://`）
- サーバが起動しているか（`make run/grpc`）
- 実機の場合、**平文通信がプラットフォームにブロックされていないか**。
  Android は API 28 以降 cleartext 通信が既定で不許可、iOS は ATS が既定で平文を拒否する。
  ローカル検証で例外設定を入れるにしても、恒久策は TLS 対応（ROADMAP 側の課題）。
- Android は `INTERNET` パーミッションが要る。

### `codes.Unavailable` が返る

サーバへ到達できていないか、ランキングが Redis に未構築。後者ならリポジトリ側で
`make load/warm` を実行して再構築する（HTTP 版の 503 と同じ状態）。
**空配列を返す実装にして誤魔化さない**のが仕様上の意図。詳細は
[docs/testing/ranking.md](../../docs/testing/ranking.md) §0。

### エディタの再生を止めても接続やストリームが残る

`OnDestroy` での後始末を書いていない。Unity はドメインリロードを跨いで
ネイティブ側の接続を回収してくれないため、再生のたびに積み上がって最終的にエディタが固まる。

`RankingClientSample.OnDestroy` のとおり、**`CancellationTokenSource.Cancel()` →
`GrpcChannel.Dispose()`** を必ず行う。streaming を購読している場合、
Cancel を飛ばさないと `await foreach` のループが生き残る。

### 「Unity gRPC」で見つけた記事のとおりにやると動かない

`Grpc.Core`（C-core）前提の古い記事の可能性が高い。上記「なぜこの構成なのか」§1 を参照。

---

## `.proto` を変更したとき

C# 生成物は `.proto` から自動生成される。**手で編集しない。**

```bash
make proto/gen/csharp    # 要ネットワーク（buf の remote plugin を使う）
```

生成物と `.proto` のダイジェスト（`.proto-digest`）をコミットする。
`make proto/gen/check` が「`.proto` だけ変えて再生成を忘れている」状態を検知する
（既知の検出漏れも含め、詳細は `make/proto.mk` のコメント）。

契約の正本は `proto/game/ranking/v1/ranking.proto` であって、この C# ではない。
RPC 名・フィールドを知りたいときは `.proto` を読むこと。

---

## 現状の制約

- **TLS 未対応。** サーバは平文 h2c のみ。実機・本番で使うには TLS 対応が要る（ROADMAP 側の課題）
  <!-- ssot-assert: absent-grep 'credentials' internal/infrastructure/server/grpc.go -->
- 認証・認可は未実装（メタデータでのトークン付与も未整備）
  <!-- ssot-assert: manual '認証インターセプタの「不在」は字面で照合できない。インターセプタの命名規約が無く、absent-grep で書くと将来どんな名前を付けても検知漏れになるため。認証を入れるときは interceptor.go・NewGRPC の登録・本項を同時に更新する' -->
- リトライ / デッドライン既定値のポリシーは未整備。サンプルでは unary に個別に deadline を入れている
  <!-- ssot-assert: manual '「ポリシーが未整備」は不在の宣言ではなく整備状況の記述なので字面で照合できない。サンプルが個別 deadline を持つことは present-grep で照合できるが、それはこの項の主張ではない。gRPC の service config やリトライポリシーを導入するときに本項を更新する' -->
- 動作確認はリポジトリオーナーが Unity エディタ上で別途行う。ここにあるのはコードと手順のみ

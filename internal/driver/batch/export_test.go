package batch

// OutboxGCChunkSize は非公開の outboxGCChunkSize を外部テストへ公開する seam。
//
// テストは「チャンクが埋まったら次のチャンクへ進む／埋まらなければ終了する」という
// 境界の振る舞いを検証するため、境界そのものの値を知る必要がある。
// テスト側に同じ数値を書くと実装との二重管理になり、片方だけ変えたときに
// 検証の意味が静かに失われるため、実装の定数をそのまま参照させる。
const OutboxGCChunkSize = outboxGCChunkSize

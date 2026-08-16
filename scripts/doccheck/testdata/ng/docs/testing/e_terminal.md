# 図の終端を通らないケースがある

```mermaid
flowchart TD
    A[開始] --> B{入力が妥当か}
    B -- No --> E1((エラー))
    B -- Yes --> Z([完了])
```

<!-- testcases: internal/sample/sample_test.go#TestTerminal -->

| # | 条件 | 図のパス | 期待結果 |
| --- | --- | --- | --- |
| 1 | エラー側だけ | `A→B→E1` | エラー |

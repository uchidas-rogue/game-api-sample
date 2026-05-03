// このファイルは sqlc 生成ファイルではなく、モック生成ディレクティブを置くためのもの。
// sqlc が自動生成する querier.go は DO NOT EDIT のため、go:generate を本ファイルに集約する。
package sqlc

//go:generate mockgen -source=querier.go -destination=mock/mock_querier.go -package=mock_sqlc

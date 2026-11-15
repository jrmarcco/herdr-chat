// Package herdrchat 承载仓库级的嵌入资源。
//
// 起手配置的正本在仓库根的 config.env.example；
// `go:embed` 只能嵌包目录内的文件，所以由本包 ( 也在根上 ) 嵌入，
// internal/cli 经此取用 —— `herdrchat init` 把它逐字节写成 profile 的 config.env。
package herdrchat

import _ "embed"

// ConfigEnvExample 是 config.env.example 全文的嵌入副本。
//
//go:embed config.env.example
//nolint:gochecknoglobals // go:embed 只能落在包级变量上。
var ConfigEnvExample []byte

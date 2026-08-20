// Package web 把前端构建产物嵌进二进制。
//
// 前端源码在仓库根的 web/ 下独立开发（Vue 3 + Vite），
// 构建产物输出到本包的 dist/（见 web/vite.config.ts 的 outDir），
// 由此处 embed 进最终的可执行文件——部署时只需要 scp 一个文件。
package web

import (
	"embed"
	"io/fs"
)

// all: 前缀让以 _ 和 . 开头的文件也被打包进去（Vite 会产出这类文件名）。
//
//go:embed all:dist
var dist embed.FS

// FS 返回以 dist 为根的文件系统。
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

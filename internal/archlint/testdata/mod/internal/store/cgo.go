//go:build ignore

// 任何配置都不编译的文件也要查。
package store

import "C" // want nocgo

import "bugu/internal/exec" // want imports

var _ = "gemini" // want vendornames

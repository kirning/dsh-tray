// Package platform 隔离所有操作系统相关的系统调用。
//
// 约定：本文件只放平台无关的辅助代码；每个平台各有一个实现文件
// （sys_windows.go / sys_linux.go），文件名后缀保证 Go 只在对应平台编译它。
// 新增能力时必须两个平台都实现，否则另一平台会编译失败——这是刻意的，
// 避免出现“能编译但运行时才发现没实现”的情况。
package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var pctEnvRe = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)

// ExpandEnv 展开 %VAR% 与 $VAR 两种写法。
// Go 的 os.ExpandEnv 只认 $VAR，而 Windows 用户习惯粘贴 %LOCALAPPDATA%\...，
// 这里两种都支持；查不到的变量原样保留，便于用户看出是哪个变量写错了。
func ExpandEnv(s string) string {
	if s == "" {
		return s
	}
	out := os.Expand(s, func(k string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		return "$" + k
	})
	return pctEnvRe.ReplaceAllStringFunc(out, func(m string) string {
		name := m[1 : len(m)-1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}

// NormalizeExecutablePath 处理用户粘贴的路径：去引号/空白、展开变量、
// 只给目录时补上平台可执行文件名。
func NormalizeExecutablePath(raw, name string) (string, error) {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, `"'`)
	p = ExpandEnv(p)
	if p == "" {
		return "", os.ErrInvalid
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		if cand, ok := FindExecutable(filepath.Join(p, name)); ok {
			return cand, nil
		}
		return filepath.Join(p, name), nil
	}
	if cand, ok := FindExecutable(p); ok {
		return cand, nil
	}
	return p, nil
}

// FindExecutable 对给定路径补齐平台可执行后缀并确认存在。
func FindExecutable(p string) (string, bool) {
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	for _, ext := range executableExts() {
		if ext == "" {
			continue
		}
		q := p + ext
		if st, err := os.Stat(q); err == nil && !st.IsDir() {
			return q, true
		}
	}
	return "", false
}

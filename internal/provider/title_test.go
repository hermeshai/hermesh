package provider

import (
	"strings"
	"testing"
)

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"修复Safari登录按钮无响应并补充vitest测试\n":           "修复Safari登录按钮无响应并补充vitest测试",
		"\n\n  \"Fix login on Safari.\"  \nmore": "Fix login on Safari",
		"“修复 Safari 登录”。":                        "修复 Safari 登录",
		"`Add retries`":                          "Add retries",
		"  \n \t":                                "",
		"Fix the title: it's cut off":            "Fix the title: it's cut off",
		strings.Repeat("修", 50):                  strings.Repeat("修", 40) + "…",
		"# 任务：修复登录页\n细节……":                       "任务：修复登录页",
		"## **修复** `calc.js` 里的 *add*":           "修复 calc.js 里的 add",
		"> - [x] 看一下 [文档](https://example.com/doc) 的 ~~旧~~ 写法": "看一下 文档 的 旧 写法",
		"1. Refactor `user_service.go` and __init__.py":        "Refactor user_service.go and __init__.py",
		"```go\nfmt.Println(1)\n```":                           "fmt.Println(1)",
		"---\n\n* * *\nFix 2*3 and a*b*c":                      "Fix 2*3 and a*b*c",
		"![截图](a.png) 按图改":                                     "截图 按图改",
		"#123 issue: crash on start":                           "#123 issue: crash on start",
	} {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

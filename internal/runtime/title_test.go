package runtime

import (
	"strings"
	"testing"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
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

func TestNormalizeLocale(t *testing.T) {
	cases := map[string]string{
		"zh-CN":       "zh-CN",
		"zh_CN.UTF-8": "zh-CN",
		"zh-Hans-CN":  "zh-CN",
		"zh":          "zh-CN",
		"zh-Hant-HK":  "zh-TW",
		"zh_TW":       "zh-TW",
		"en_US.UTF-8": "en",
		"en-GB":       "en",
		"ja_JP":       "ja",
		"ko-KR":       "ko",
		"fr_FR":       "fr-FR",
		"C":           "",
		"POSIX":       "",
		"system":      "",
		"":            "",
	}
	for in, want := range cases {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveSeedTitleLocaleShortCircuit(t *testing.T) {
	// 1. Chinese client + clean Chinese + code identifier -> short-circuits as final!
	title, final := DeriveSeedTitle("修复 WebSocket 重连竞态", nil, "zh-CN")
	if title != "修复 WebSocket 重连竞态" || !final {
		t.Fatalf("zh-CN clean prompt: got (%q, %v), want (\"修复 WebSocket 重连竞态\", true)", title, final)
	}

	// 2. Chinese client + English prompt -> seed kept for 0ms display, MUST NOT short-circuit!
	title, final = DeriveSeedTitle("Fix WebSocket reconnect race", nil, "zh-CN")
	if title != "Fix WebSocket reconnect race" || final {
		t.Fatalf("zh-CN client with English prompt must not short-circuit: got (%q, %v)", title, final)
	}

	// 3. English client + Chinese prompt -> seed kept for 0ms display, MUST NOT short-circuit!
	title, final = DeriveSeedTitle("修复托盘图标闪烁", nil, "en")
	if title != "修复托盘图标闪烁" || final {
		t.Fatalf("en client with Chinese prompt must not short-circuit: got (%q, %v)", title, final)
	}

	// 4. English client + clean English short command -> short-circuits as final!
	title, final = DeriveSeedTitle("Fix tray icon flicker", nil, "en")
	if title != "Fix tray icon flicker" || !final {
		t.Fatalf("en clean prompt: got (%q, %v), want (\"Fix tray icon flicker\", true)", title, final)
	}

	// 5. Vague deictic prompts never short-circuit.
	if _, final := DeriveSeedTitle("修一下这个报错", nil, "zh-CN"); final {
		t.Fatalf("vague zh-CN prompt should not short-circuit")
	}
	if _, final := DeriveSeedTitle("Fix this failing test", nil, "en"); final {
		t.Fatalf("vague en prompt should not short-circuit")
	}

	// 6. URL & path compaction + attachment localized fallbacks.
	urlSeed, final := DeriveSeedTitle("Review https://github.com/pingdotgg/t3code/pull/10462", nil, "zh-CN")
	if urlSeed != "Review t3code#10462" || final {
		t.Fatalf("URL seed compaction: got (%q, %v)", urlSeed, final)
	}
	imgSeed, final := DeriveSeedTitle("", []*sessionv1.AttachmentRef{{Name: "bug.png", MimeType: "image/png"}}, "zh-CN")
	if imgSeed != "图片任务" || final {
		t.Fatalf("zh-CN image fallback: got (%q, %v)", imgSeed, final)
	}
	imgSeedEN, _ := DeriveSeedTitle("", []*sessionv1.AttachmentRef{{Name: "bug.png", MimeType: "image/png"}}, "en")
	if imgSeedEN != "Attached Image" {
		t.Fatalf("en image fallback: got %q", imgSeedEN)
	}
}

func TestParseTitleGenOutput(t *testing.T) {
	res, err := ParseTitleGenOutput("```json\n{\"title\": \"\\\"修复 Safari 登录重定向。\\\"\", \"provisional\": true}\n```")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Title != "修复 Safari 登录重定向" || !res.Provisional {
		t.Fatalf("got %+v", res)
	}
	if _, err := ParseTitleGenOutput(`{"title": "title", "provisional": false}`); err == nil {
		t.Fatalf("expected placeholder 'title' to be rejected")
	}
}

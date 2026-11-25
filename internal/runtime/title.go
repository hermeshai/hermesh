package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

const TitleGenSchema = `{"type":"object","properties":{"title":{"type":"string"},"provisional":{"type":"boolean"}},"required":["title","provisional"],"additionalProperties":false}`

var (
	rawURLRE     = regexp.MustCompile(`https?://[^\s<>()"'\[\]` + "`" + `]+`)
	absPathRE    = regexp.MustCompile(`(?:^|[\s("'` + "`" + `])(/(?:Users|home|var|tmp|opt|etc|private|Volumes)/[^\s)"'` + "`" + `]+|[A-Za-z]:\\[^\s)"'` + "`" + `]+)`)
	stackTraceRE = regexp.MustCompile(`(?i)(^panic:|^traceback\b|exception:|^\s*at\s+\S+\s*\(|goroutine\s+\d+\s*\[|error\[\w+\]:|exit status \d+)`)
	vagueZHRE    = regexp.MustCompile(`^(修一下|改一下|看一下|看看|帮我看看|处理一下|解决一下)?(这个|那个|这里|那里|它|这|那)(问题|报错|错误|bug|测试|单测|情况|页面|样式)?[？?！!。.]*$`)
	vagueENRE    = regexp.MustCompile(`(?i)^(please\s+)?(fix|check|look at|debug|investigate|resolve|update|help with)\s+(this|that|it|these|those|here)(\s+(bug|error|issue|problem|test|failing test|crash|page|thing))?[?!.]*$`)
)

// NormalizeLocale canonicalizes a BCP 47 or POSIX locale string (such as
// "zh_CN.UTF-8", "zh-Hans-CN", "en-US", "ja_JP") into a clean BCP 47 tag.
// Returns "" when unset or C/POSIX.
func NormalizeLocale(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, ".@"); i >= 0 {
		s = s[:i]
	}
	s = strings.ReplaceAll(strings.TrimSpace(s), "_", "-")
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	if lower == "c" || lower == "posix" || lower == "system" || lower == "default" {
		return ""
	}
	parts := strings.Split(lower, "-")
	lang := parts[0]
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return ""
		}
	}
	if len(lang) < 2 || len(lang) > 3 {
		return ""
	}
	rest := parts[1:]
	switch lang {
	case "zh":
		for _, p := range rest {
			if p == "hant" || p == "tw" || p == "hk" || p == "mo" {
				return "zh-TW"
			}
		}
		return "zh-CN"
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "ko":
		return "ko"
	default:
		if len(rest) > 0 && len(rest[0]) == 2 {
			return lang + "-" + strings.ToUpper(rest[0])
		}
		return lang
	}
}

// DefaultLocaleFromEnv resolves the process locale from environment variables,
// defaulting to "en".
func DefaultLocaleFromEnv() string {
	for _, k := range []string{"HERMESH_LANG", "HER_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if loc := NormalizeLocale(os.Getenv(k)); loc != "" {
			return loc
		}
	}
	return "en"
}

// ResolveLocale returns the first valid normalized locale from candidates,
// falling back to DefaultLocaleFromEnv().
func ResolveLocale(candidates ...string) string {
	for _, c := range candidates {
		if loc := NormalizeLocale(c); loc != "" {
			return loc
		}
	}
	return DefaultLocaleFromEnv()
}

// LocaleDisplayName returns the English language name and length guidance used
// in title generation prompts for a normalized BCP 47 locale.
func LocalePromptSpec(locale string) (langName string, lengthRule string) {
	loc := NormalizeLocale(locale)
	switch loc {
	case "zh-CN":
		return "Simplified Chinese (简体中文)", "6-16 Chinese characters (紧凑动宾或名词短语，禁用“关于/进行/问题/帮忙/请”等冗余词)"
	case "zh-TW":
		return "Traditional Chinese (繁體中文)", "6-16 Chinese characters (緊湊動賓或名詞短語，禁用冗餘套話)"
	case "ja":
		return "Japanese (日本語)", "8-20 characters (簡潔な体言止めまたは動詞句)"
	case "ko":
		return "Korean (한국어)", "2-5 words, at most 18 characters (간결한 명사구)"
	case "fr":
		return "French (français)", "3-7 words, fewer than 42 characters"
	case "de":
		return "German (Deutsch)", "3-6 words, fewer than 42 characters"
	case "es":
		return "Spanish (español)", "3-7 words, fewer than 42 characters"
	case "en", "":
		return "English", "3-7 words, fewer than 38 characters"
	default:
		return fmt.Sprintf("the language of locale %q", loc), "3-7 words, fewer than 40 characters"
	}
}

// LocalizedSeedFallback returns a localized placeholder title for empty,
// attachment-only, or forked sessions.
func LocalizedSeedFallback(locale, kind, name string) string {
	loc := NormalizeLocale(locale)
	switch kind {
	case "image":
		switch loc {
		case "zh-CN":
			return "图片任务"
		case "zh-TW":
			return "圖片任務"
		case "ja":
			return "画像タスク"
		case "ko":
			return "이미지 작업"
		default:
			return "Attached Image"
		}
	case "file":
		clean := CleanTitle(name)
		if clean == "" {
			clean = "attachment"
		}
		switch loc {
		case "zh-CN":
			return CleanTitle("文件：" + clean)
		case "zh-TW":
			return CleanTitle("檔案：" + clean)
		case "ja":
			return CleanTitle("ファイル: " + clean)
		case "ko":
			return CleanTitle("파일: " + clean)
		default:
			return CleanTitle("File: " + clean)
		}
	case "fork":
		base := strings.TrimSpace(name)
		if base == "" {
			base = LocalizedSeedFallback(loc, "empty", "")
		}
		switch loc {
		case "zh-CN", "zh-TW":
			return CleanTitle(base + "（分支）")
		case "ja":
			return CleanTitle(base + " (分岐)")
		case "ko":
			return CleanTitle(base + " (분기)")
		default:
			return CleanTitle(base + " (fork)")
		}
	default: // "empty"
		switch loc {
		case "zh-CN":
			return "新任务"
		case "zh-TW":
			return "新任務"
		case "ja":
			return "新しいタスク"
		case "ko":
			return "새 작업"
		default:
			return "New Task"
		}
	}
}

// IsPlaceholderSeedTitle reports whether title is a default empty placeholder.
func IsPlaceholderSeedTitle(title string) bool {
	switch strings.TrimSpace(title) {
	case "", "New Session", "New Task", "New thread", "新任务", "新任務", "新しいタスク", "새 작업":
		return true
	}
	return false
}

// TitleMatchesLocaleScript reports whether an explicit agent-supplied title
// already matches the writing system of locale, so foreign-script agent titles
// can be refined into the client's UI language.
func TitleMatchesLocaleScript(title, locale string) bool {
	loc := NormalizeLocale(locale)
	var hasHan, hasKana, hasHangul, hasASCIILetter bool
	for _, r := range title {
		switch {
		case unicode.Is(unicode.Han, r):
			hasHan = true
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			hasKana = true
		case unicode.Is(unicode.Hangul, r):
			hasHangul = true
		case r <= unicode.MaxASCII && unicode.IsLetter(r):
			hasASCIILetter = true
		}
	}
	switch loc {
	case "zh-CN", "zh-TW":
		return hasHan && !hasKana && !hasHangul
	case "en":
		return hasASCIILetter && !hasHan && !hasKana && !hasHangul
	case "ja":
		return (hasKana || hasHan) && !hasHangul
	case "ko":
		return hasHangul
	default:
		return false
	}
}

func compactURLForSeed(raw string) string {
	trimmed := strings.TrimRight(raw, ".,:;!?*)")
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return ""
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	// https://github.com/org/repo/pull/123 or /issues/123 -> repo#123
	if len(segs) >= 4 && (segs[2] == "pull" || segs[2] == "issues" || segs[2] == "merge_requests") {
		return segs[1] + "#" + segs[3]
	}
	if len(segs) >= 2 {
		return segs[len(segs)-2] + "/" + segs[len(segs)-1]
	}
	if len(segs) == 1 {
		return u.Host + "/" + segs[0]
	}
	return u.Host
}

func compactPromptForSeed(prompt string) string {
	var kept []string
	inFence := false
	for _, line := range strings.Split(prompt, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || t == "" || stackTraceRE.MatchString(t) {
			continue
		}
		t = rawURLRE.ReplaceAllStringFunc(t, compactURLForSeed)
		t = absPathRE.ReplaceAllStringFunc(t, func(m string) string {
			lead := ""
			if len(m) > 0 && (m[0] == ' ' || m[0] == '\t' || m[0] == '(' || m[0] == '"' || m[0] == '\'' || m[0] == '`') {
				lead = m[:1]
				m = m[1:]
			}
			base := filepath.Base(strings.ReplaceAll(m, `\`, "/"))
			if base == "" || base == "." || base == "/" {
				return lead + m
			}
			return lead + base
		})
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		kept = append(kept, t)
		// Only pull in the next line when the first line is a short intro ending
		// in a colon (e.g. "Fix this:\nhttps://github.com/org/repo/pull/42").
		if len(kept) == 1 && utf8.RuneCountInString(t) <= 16 && (strings.HasSuffix(t, ":") || strings.HasSuffix(t, "：")) {
			continue
		}
		break
	}
	return CleanTitle(strings.Join(kept, " "))
}

// DeriveSeedTitle computes an immediate 0ms title from the user's prompt and
// attachments, and reports whether the seed is already a clean, target-locale
// title that needs no LLM pass (isFinal == true).
func DeriveSeedTitle(prompt string, attachments []*sessionv1.AttachmentRef, locale string) (title string, isFinal bool) {
	loc := ResolveLocale(locale)
	seed := compactPromptForSeed(prompt)
	if seed != "" {
		return seed, canShortCircuitTitle(seed, prompt, attachments, loc)
	}
	for _, a := range attachments {
		if strings.HasPrefix(strings.ToLower(a.GetMimeType()), "image/") {
			return LocalizedSeedFallback(loc, "image", ""), false
		}
	}
	if len(attachments) > 0 && strings.TrimSpace(attachments[0].GetName()) != "" {
		return LocalizedSeedFallback(loc, "file", attachments[0].GetName()), false
	}
	return LocalizedSeedFallback(loc, "empty", ""), false
}

func canShortCircuitTitle(seed, rawPrompt string, attachments []*sessionv1.AttachmentRef, locale string) bool {
	if len(attachments) > 0 {
		return false
	}
	trimmed := strings.TrimSpace(rawPrompt)
	if strings.Contains(trimmed, "\n") || rawURLRE.MatchString(trimmed) || stackTraceRE.MatchString(trimmed) {
		return false
	}
	if strings.ContainsAny(seed, "？?") {
		return false
	}
	var hanCount, kanaHangulCount, nonASCIILetterCount, asciiLetterCount int
	for _, r := range seed {
		switch {
		case unicode.Is(unicode.Han, r):
			hanCount++
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r):
			kanaHangulCount++
		case r <= unicode.MaxASCII && unicode.IsLetter(r):
			asciiLetterCount++
		case r > unicode.MaxASCII && unicode.IsLetter(r):
			nonASCIILetterCount++
		}
	}
	runes := utf8.RuneCountInString(seed)
	switch locale {
	case "zh-CN", "zh-TW":
		if hanCount < 2 || hanCount > 16 || runes < 4 || runes > 22 || kanaHangulCount > 0 || nonASCIILetterCount > 0 {
			return false
		}
		if vagueZHRE.MatchString(seed) {
			return false
		}
		return true
	case "en":
		if asciiLetterCount < 3 || hanCount > 0 || kanaHangulCount > 0 || nonASCIILetterCount > 0 {
			return false
		}
		words := strings.Fields(seed)
		if len(words) < 2 || len(words) > 6 || runes < 6 || runes > 38 {
			return false
		}
		if vagueENRE.MatchString(seed) {
			return false
		}
		return true
	default:
		// For ja, ko, fr, de, es, etc., always run Pass 1 so the model guarantees
		// exact target-language phrasing.
		return false
	}
}

// SanitizeGeneratedTitle cleans a model-produced title string and validates it.
func SanitizeGeneratedTitle(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	// Unwrap accidental nested JSON {"title": "..."} before cleaning.
	if strings.HasPrefix(s, "{") {
		var nested struct {
			Title string `json:"title"`
		}
		if json.Unmarshal([]byte(s), &nested) == nil && strings.TrimSpace(nested.Title) != "" {
			s = nested.Title
		}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || mdSkip.MatchString(t) {
			continue
		}
		t = mdBlock.ReplaceAllString(t, "")
		t = mdLink.ReplaceAllString(t, "$1")
		t = mdStrong.ReplaceAllString(t, "$2")
		t = mdEm.ReplaceAllString(t, "$1$2")
		t = strings.ReplaceAll(t, "`", "")
		t = mdSpace.ReplaceAllString(strings.TrimSpace(t), " ")
		t = strings.TrimSpace(strings.Trim(t, "\"'“”‘’「」『』"))
		t = strings.TrimSpace(strings.TrimRight(t, "。.!！?？;；,，"))
		if t == "" {
			continue
		}
		if strings.EqualFold(t, "title") || IsPlaceholderSeedTitle(t) {
			return "", errors.New("model returned placeholder title")
		}
		if r := []rune(t); len(r) > 60 {
			return strings.TrimSpace(string(r[:60])) + "…", nil
		}
		return t, nil
	}
	return "", errors.New("empty generated title")
}

// ParseTitleGenOutput extracts and validates a TitleGenResult JSON object from
// raw model text (handling optional markdown code fences or surrounding prose).
func ParseTitleGenOutput(raw string) (TitleGenResult, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return TitleGenResult{}, errors.New("empty title generation output")
	}
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return TitleGenResult{}, errors.New("no JSON object in title generation output")
	}
	var parsed TitleGenResult
	if err := json.Unmarshal([]byte(s[start:end+1]), &parsed); err != nil {
		return TitleGenResult{}, fmt.Errorf("decode title JSON: %w", err)
	}
	clean, err := SanitizeGeneratedTitle(parsed.Title)
	if err != nil {
		return TitleGenResult{}, err
	}
	return TitleGenResult{
		Title:       clean,
		Provisional: parsed.Provisional,
	}, nil
}

// SliceHeadTail truncates s to maxRunes by preserving both the opening and
// closing halves when s exceeds maxRunes, avoiding lone UTF-8/surrogate cuts.
func SliceHeadTail(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxRunes || maxRunes <= 16 {
		if len(r) <= maxRunes {
			return s
		}
		return string(r[:maxRunes])
	}
	const marker = "\n[...]\n"
	avail := maxRunes - len([]rune(marker))
	head := (avail + 1) / 2
	tail := avail - head
	return strings.TrimSpace(string(r[:head])) + marker + strings.TrimSpace(string(r[len(r)-tail:]))
}

var (
	fakeLocaleLineRE = regexp.MustCompile(`(?m)^Target locale:\s*(\S+)`)
	fakeFilesLineRE  = regexp.MustCompile(`(?m)^-\s+([^\s(]+)`)
	fakeIdentTokenRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_./-]{2,30}`)
)

// FakeGenerateTitle produces deterministic, locale-accurate titles for unit
// and E2E tests without calling an external LLM.
func FakeGenerateTitle(req TitleGenRequest) TitleGenResult {
	locale := "en"
	if m := fakeLocaleLineRE.FindStringSubmatch(req.Prompt); len(m) == 2 {
		locale = NormalizeLocale(m[1])
	}
	isPass2 := strings.Contains(req.Prompt, "Mode: refine") || strings.Contains(req.Prompt, "Mode: regenerate")

	var touchedFile string
	if idx := strings.Index(req.Prompt, "Touched files:"); idx >= 0 {
		section := req.Prompt[idx:]
		if m := fakeFilesLineRE.FindStringSubmatch(section); len(m) == 2 {
			touchedFile = filepath.Base(m[1])
		}
	}

	userMsg := extractPromptSection(req.Prompt, "User message:")
	if userMsg == "" {
		userMsg = extractPromptSection(req.Prompt, "Conversation:")
	}

	vague := strings.TrimSpace(userMsg) == "" ||
		strings.Contains(strings.ToLower(userMsg), "fix this") ||
		strings.Contains(strings.ToLower(userMsg), "failing test") ||
		strings.Contains(userMsg, "修一下这个") ||
		strings.Contains(userMsg, "看看这个报错")

	var subject string
	if touchedFile != "" {
		subject = touchedFile
	} else {
		tokens := fakeIdentTokenRE.FindAllString(userMsg, -1)
		for _, preferPath := range []bool{true, false} {
			for _, tok := range tokens {
				low := strings.ToLower(tok)
				switch low {
				case "fix", "this", "that", "the", "and", "for", "with", "error", "bug", "issue", "test", "failing", "please", "user", "say", "ask", "write", "shell", "sleep", "https", "http", "nil", "pointer", "panic":
					continue
				}
				if preferPath && !strings.ContainsAny(tok, "._/") {
					continue
				}
				subject = filepath.Base(tok)
				break
			}
			if subject != "" {
				break
			}
		}
	}

	provisional := !isPass2 && vague && touchedFile == ""
	return TitleGenResult{
		Title:       formatFakeTitle(locale, subject, vague, len(req.Images) > 0),
		Provisional: provisional,
	}
}

func extractPromptSection(prompt, header string) string {
	idx := strings.Index(prompt, header)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(prompt[idx+len(header):])
	if cut := strings.Index(rest, "\n\n"); cut >= 0 {
		return strings.TrimSpace(rest[:cut])
	}
	return rest
}

func formatFakeTitle(locale, subject string, vague, hasImage bool) string {
	switch locale {
	case "zh-CN":
		if subject != "" {
			return "修复 " + subject + " 问题"
		}
		if hasImage {
			return "排查截图界面问题"
		}
		if vague {
			return "排查待定位异常"
		}
		return "更新项目逻辑"
	case "zh-TW":
		if subject != "" {
			return "修復 " + subject + " 問題"
		}
		if hasImage {
			return "排查截圖介面問題"
		}
		return "更新專案邏輯"
	case "ja":
		if subject != "" {
			return subject + " の修正"
		}
		return "タスクの調査と修正"
	case "ko":
		if subject != "" {
			return subject + " 문제 수정"
		}
		return "작업 조사 및 수정"
	default:
		if subject != "" {
			return "Fix " + subject + " issue"
		}
		if hasImage {
			return "Investigate UI screenshot"
		}
		if vague {
			return "Investigate reported issue"
		}
		return "Update project logic"
	}
}

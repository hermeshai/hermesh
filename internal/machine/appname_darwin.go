package machine

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/unix"
	"howett.net/plist"
)

// appName is the name the person sees for the app process pid belongs to
// (in Finder, the Dock, Activity Monitor): its bundle's display name in the
// user's language ("千问办公", not the executable's "QwenWorkCN"), or ""
// when pid is not in an app. Read from files: nothing is started.
func appName(pid int32) string {
	app := appBundle(executable(pid))
	if app == "" {
		return ""
	}
	res := filepath.Join(app, "Contents/Resources")
	// Apple's own apps keep every language in one table.
	var table map[string]bundleInfo
	readPlist(filepath.Join(res, "InfoPlist.loctable"), &table)
	for _, l := range lprojs(userLanguages()) {
		if name := table[l].name(); name != "" {
			return name
		}
		var info bundleInfo
		if readPlist(filepath.Join(res, l+".lproj/InfoPlist.strings"), &info); info.name() != "" {
			return info.name()
		}
	}
	var info bundleInfo
	if readPlist(filepath.Join(app, "Contents/Info.plist"), &info); info.name() != "" {
		return info.name()
	}
	return strings.TrimSuffix(filepath.Base(app), ".app")
}

// executable is pid's executable path, from kern.procargs2: the argument
// count, then the path.
func executable(pid int32) string {
	b, err := unix.SysctlRaw("kern.procargs2", int(pid))
	if err != nil || len(b) < 4 {
		return ""
	}
	path, _, _ := bytes.Cut(b[4:], []byte{0})
	return string(path)
}

// appBundle is the outermost .app holding path (a helper app's host), or "".
func appBundle(path string) string {
	if i := strings.Index(path, ".app/"); i >= 0 {
		return path[:i+len(".app")]
	}
	return ""
}

// userLanguages is the user's preferred languages ("zh-Hans-CN", "en").
func userLanguages() []string {
	home, _ := os.UserHomeDir()
	var prefs struct {
		AppleLanguages []string
	}
	readPlist(filepath.Join(home, "Library/Preferences/.GlobalPreferences.plist"), &prefs)
	return prefs.AppleLanguages
}

// lprojs are the .lproj names a bundle may use for langs, best first:
// "zh-Hans-CN" gives zh-Hans-CN, zh_Hans_CN, zh-Hans, zh_Hans, zh_CN, zh.
func lprojs(langs []string) []string {
	var out []string
	add := func(l string) {
		for _, v := range []string{l, strings.ReplaceAll(l, "-", "_")} {
			if !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	for _, lang := range langs {
		parts := strings.Split(lang, "-")
		for n := len(parts); n > 0; n-- {
			add(strings.Join(parts[:n], "-"))
			if n == 2 && parts[0] == "zh" {
				// Older bundles name Chinese by region.
				switch parts[1] {
				case "Hans":
					add("zh-CN")
				case "Hant":
					add("zh-TW")
				}
			}
		}
	}
	return out
}

type bundleInfo struct {
	CFBundleDisplayName, CFBundleName string
}

// name is CFBundleDisplayName, else CFBundleName.
func (b bundleInfo) name() string {
	name := b.CFBundleDisplayName
	if name == "" {
		name = b.CFBundleName
	}
	// Some names carry a direction mark ("\u200eWhatsApp").
	return strings.TrimSpace(strings.Trim(name, "\u200e\u200f"))
}

// readPlist decodes a property list file (binary, XML or text; UTF-8 or
// UTF-16) into v, leaving it as it was when the file can't be read.
func readPlist(file string, v any) {
	if b, err := os.ReadFile(file); err == nil {
		plist.Unmarshal(fromUTF16(b), v)
	}
}

// fromUTF16 converts UTF-16 text with a byte order mark to UTF-8.
func fromUTF16(b []byte) []byte {
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(b, []byte{0xff, 0xfe}):
		order = binary.LittleEndian
	case bytes.HasPrefix(b, []byte{0xfe, 0xff}):
		order = binary.BigEndian
	default:
		return b
	}
	u := make([]uint16, (len(b)-2)/2)
	for i := range u {
		u[i] = order.Uint16(b[2+2*i:])
	}
	return []byte(string(utf16.Decode(u)))
}

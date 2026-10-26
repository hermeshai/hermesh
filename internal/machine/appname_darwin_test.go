package machine

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fakeApp makes name.app around a copy of /bin/sleep, with the given
// files under Contents, and starts it.
func fakeApp(t *testing.T, name string, files map[string]string) *exec.Cmd {
	t.Helper()
	app := filepath.Join(t.TempDir(), name+".app")
	bin := filepath.Join(app, "Contents/MacOS/sleeper")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	b, err := os.ReadFile("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(bin, b, 0o755)
	// A copy of a system binary runs only signed again.
	if out, err := exec.Command("codesign", "-f", "-s", "-", bin).CombinedOutput(); err != nil {
		t.Fatalf("codesign: %v %s", err, out)
	}
	for f, s := range files {
		p := filepath.Join(app, "Contents", f)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	cmd := exec.Command(bin, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	time.Sleep(100 * time.Millisecond)
	return cmd
}

// preferLanguages makes the user prefer langs (HOME is the test's).
func preferLanguages(t *testing.T, langs string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, "Library/Preferences"), 0o755)
	os.WriteFile(filepath.Join(home, "Library/Preferences/.GlobalPreferences.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>AppleLanguages</key><array>`+langs+`</array></dict></plist>`), 0o644)
}

func utf16le(s string) string {
	b := []byte{0xff, 0xfe}
	for _, r := range s {
		b = append(b, byte(r), byte(r>>8))
	}
	return string(b)
}

// A process is named as the person sees its app: the bundle's display
// name in their language, whatever the executable is called.
func TestAppName(t *testing.T) {
	info := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleName</key><string>QwenWorkCN</string></dict></plist>`
	cmd := fakeApp(t, "QwenWorkCN", map[string]string{
		"Info.plist": info,
		// Text, UTF-8 (as this app has it)…
		"Resources/zh-Hans.lproj/InfoPlist.strings": "\"CFBundleDisplayName\" = \"千问办公\";\n\"CFBundleName\" = \"千问办公\";\n",
		// …and UTF-16 (as others do).
		"Resources/ja.lproj/InfoPlist.strings": utf16le("CFBundleName = \"千問オフィス\";\n"),
	})
	pid := int32(cmd.Process.Pid)
	for _, c := range []struct{ langs, want string }{
		{"<string>zh-Hans-CN</string>", "千问办公"},
		{"<string>ja-JP</string><string>zh-Hans-CN</string>", "千問オフィス"},
		{"<string>de-DE</string>", "QwenWorkCN"},
	} {
		preferLanguages(t, c.langs)
		if got := appName(pid); got != c.want {
			t.Errorf("%s: appName = %q, want %q", c.langs, got, c.want)
		}
	}
	// Apple's apps: one table for every language.
	preferLanguages(t, "<string>zh-Hans-CN</string>")
	cal := fakeApp(t, "Calendar", map[string]string{"Resources/InfoPlist.loctable": `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>LocProvenance</key><dict><key>zh_CN</key><integer>1</integer></dict>
<key>en</key><dict><key>CFBundleName</key><string>Calendar</string></dict>
<key>zh_CN</key><dict><key>CFBundleDisplayName</key><string>日历</string></dict>
</dict></plist>`})
	if got := appName(int32(cal.Process.Pid)); got != "日历" {
		t.Errorf("loctable: appName = %q", got)
	}
	// Without an Info.plist: the bundle's name.
	bare := fakeApp(t, "Bare Tool", nil)
	if got := appName(int32(bare.Process.Pid)); got != "Bare Tool" {
		t.Errorf("bare: appName = %q", got)
	}
	// Not in an app: nothing to say.
	if got := appName(int32(os.Getpid())); got != "" {
		t.Errorf("test binary: appName = %q", got)
	}
}

func TestLprojs(t *testing.T) {
	got := lprojs([]string{"zh-Hans-CN", "en"})
	want := []string{"zh-Hans-CN", "zh_Hans_CN", "zh-Hans", "zh_Hans", "zh-CN", "zh_CN", "zh", "en"}
	if !slices.Equal(got, want) {
		t.Fatalf("lprojs = %v", got)
	}
}

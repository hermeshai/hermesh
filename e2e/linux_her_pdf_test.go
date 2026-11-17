package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
)

// TestLinuxHerPDF (HERMESH_DOCKER=1, HERMESH_HER_PDF_LINUX=<a folder>) is Her
// reading a PDF on a Linux server (docs/linux.md §4), where her is static
// and cannot load PDFium itself. The folder holds what a release carries
// for it: her-doc-worker, her PDF worker for Linux linked against glibc
// (cargo zigbuild -p her-docs --bin her-doc-worker), and libpdfium.so
// (apps/desktop/scripts/pdfium.mjs linux). The install command puts them in
// her-pdf/ beside her, `her doctor` finds them there, and Her opens a PDF
// the owner sent her: the mock model has her open it, and what she read is
// the PDF's own line.
func TestLinuxHerPDF(t *testing.T) {
	if os.Getenv("HERMESH_DOCKER") != "1" {
		t.Skip("set HERMESH_DOCKER=1")
	}
	dir := os.Getenv("HERMESH_HER_PDF_LINUX")
	if dir == "" {
		t.Skip("set HERMESH_HER_PDF_LINUX to a folder with her-doc-worker and libpdfium.so for Linux")
	}
	var archive bytes.Buffer
	zw := gzip.NewWriter(&archive)
	tw := tar.NewWriter(zw)
	tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "./", Mode: 0o755})
	for name, mode := range map[string]int64{"her-doc-worker": 0o755, "libpdfium.so": 0o644} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "./" + name, Mode: mode, Size: int64(len(body))})
		tw.Write(body)
	}
	if tw.Close() != nil || zw.Close() != nil {
		t.Fatal("her-pdf archive")
	}
	asset := "her-pdf-linux-" + runtime.GOARCH + ".tar.gz"
	c, id, _, sh, out := linuxHerMachine(t, "hermesh-e2e-linux-server:1", linuxServerImage, "pdf-1", map[string][]byte{asset: archive.Bytes()}, "")
	if !strings.Contains(out, "› downloading "+asset) {
		t.Fatalf("install:\n%s", out)
	}
	if s, err := sh("/root/.local/bin/her doctor"); !strings.Contains(s, "pdf component: ready (/root/.local/bin/her-pdf/libpdfium.so)") {
		t.Fatalf("her doctor: %v\n%s", err, s)
	}

	// The owner sends Her a PDF, and she opens it.
	const line = "Quarterly report: revenue rose 17 percent"
	report := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(report, onePagePDF(line), 0o644); err != nil {
		t.Fatal(err)
	}
	bot := defaultBot(t, c, id)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sent, err := c.HerUpload(ctx, &localv1.HerUploadRequest{MachineId: id, BotId: bot.ID, LocalPath: report})
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := herOpen(t, ctx, c, id, herSetup("bots/open", map[string]string{"bot_id": bot.ID}))
	defer stream.Close()
	if res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: id, BotId: bot.ID, Text: "[[mock:sheet:" + sent.Path + "]] go"}); err != nil || !res.Delivered {
		t.Fatalf("HerSend: %v %v", res, err)
	}
	for stream.Receive() {
		n := stream.Msg().GetNotification()
		if n == nil || n.Method != "session/event" {
			continue
		}
		if bytes.Contains(n.Params, []byte(line)) {
			return
		}
		if bytes.Contains(n.Params, []byte("sheet done")) {
			break
		}
	}
	t.Fatalf("Her did not read the PDF's line: %v", stream.Err())
}

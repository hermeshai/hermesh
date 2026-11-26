package herhost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
)

// TitleGeneratorEnabled reports whether Her is running, supports models/title,
// and has at least one model with credentials ready. Never blocks or dials.
func (h *Host) TitleGeneratorEnabled() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stop != nil && h.snap != nil && h.snap.modelReady && h.snap.canTitle && h.mon != nil
}

// GenerateTitle runs a stateless, tool-free title prompt on Her's smallest
// available model over the already-open monitor connection.
func (h *Host) GenerateTitle(ctx context.Context, _ *sessionv1.ProviderInstanceConfig, req rt.TitleGenRequest) (rt.TitleGenResult, error) {
	if h == nil {
		return rt.TitleGenResult{}, errOff
	}
	h.mu.Lock()
	var mon *conn
	if h.stop != nil && h.snap != nil && h.snap.modelReady && h.snap.canTitle {
		mon = h.mon
	}
	h.mu.Unlock()
	if mon == nil {
		return rt.TitleGenResult{}, errors.New("her title generator not ready")
	}

	type titleImage struct {
		MimeType string `json:"mime_type"`
		Data     string `json:"data"`
	}
	var imgs []titleImage
	for _, img := range req.Images {
		if !strings.HasPrefix(strings.ToLower(img.MimeType), "image/") {
			continue
		}
		data := img.Data
		if len(data) == 0 && img.Path != "" {
			data, _ = os.ReadFile(img.Path)
		}
		if len(data) == 0 {
			continue
		}
		imgs = append(imgs, titleImage{
			MimeType: img.MimeType,
			Data:     base64.StdEncoding.EncodeToString(data),
		})
		break
	}

	payload := map[string]any{"prompt": req.Prompt}
	if len(imgs) > 0 {
		payload["images"] = imgs
	}
	params, err := json.Marshal(payload)
	if err != nil {
		return rt.TitleGenResult{}, err
	}
	res, err := mon.call(ctx, "models/title", params)
	if err := answered("models/title", res, err); err != nil {
		return rt.TitleGenResult{}, err
	}
	var out struct {
		Text  string `json:"text"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(res.Result, &out); err != nil {
		return rt.TitleGenResult{}, err
	}
	return rt.ParseTitleGenOutput(out.Text)
}

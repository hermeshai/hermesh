package scripted

import (
	"context"
	"errors"
	"os"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/runtime"
)

func (d *Driver) SetTitles() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.setTitles))
	copy(out, d.setTitles)
	return out
}

func (d *Driver) TitleGeneratorEnabled() bool {
	return d.EnableTitleGen || d.TitleFunc != nil || os.Getenv("HERMESH_FAKE_TITLE_GEN") == "1"
}

func (d *Driver) GenerateTitle(ctx context.Context, _ *sessionv1.ProviderInstanceConfig, req runtime.TitleGenRequest) (runtime.TitleGenResult, error) {
	if d.TitleFunc != nil {
		return d.TitleFunc(ctx, req)
	}
	if !d.TitleGeneratorEnabled() {
		return runtime.TitleGenResult{}, errors.New("scripted title generation disabled")
	}
	if err := ctx.Err(); err != nil {
		return runtime.TitleGenResult{}, err
	}
	return runtime.FakeGenerateTitle(req), nil
}

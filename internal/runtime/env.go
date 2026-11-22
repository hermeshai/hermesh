package runtime

import (
	"strings"

	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/internal/shellenv"
)

// BuildEnv constructs a clean subprocess environment with login-shell PATH plus
// instance-level and per-session overrides.
func BuildEnv(pathEnv string, layers ...map[string]string) []string {
	env := provider.ProbeEnv(pathEnv)
	var dropKeys []string
	for _, m := range layers {
		for k := range m {
			dropKeys = append(dropKeys, k)
		}
	}
	if len(dropKeys) > 0 {
		env = shellenv.Without(env, dropKeys...)
	}
	merged := map[string]string{}
	for _, m := range layers {
		for k, v := range m {
			if strings.TrimSpace(k) != "" {
				merged[k] = v
			}
		}
	}
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	return env
}

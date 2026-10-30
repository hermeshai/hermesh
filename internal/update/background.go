package update

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Background is hermeshd's updater, for a hermeshd its service runs. Now
// and then it looks for a newer release and, once busy (the sessions a
// restart would stop) is 0, installs it and returns true: hermeshd then
// exits, and its service starts the new program. It returns false when ctx
// is done.
func Background(ctx context.Context, dataDir, current string, busy func() int, log *slog.Logger) bool {
	wait := func(d, jitter time.Duration) bool {
		if testInterval != "" {
			d, _ = time.ParseDuration(testInterval)
		} else if jitter > 0 {
			d += rand.N(jitter)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	idle := func() bool { // false: ctx is done
		for busy() > 0 {
			if !wait(10*time.Minute, 0) {
				return false
			}
		}
		return true
	}
	if !wait(5*time.Minute, 10*time.Minute) {
		return false
	}
	for {
		if r := check(ctx, dataDir, current, log); r != nil && idle() && !Load(dataDir).Off {
			if installed(ctx, dataDir, current, r, log) && idle() {
				return true
			}
		}
		if !wait(6*time.Hour, time.Hour) {
			return false
		}
	}
}

// check returns a newer release, when automatic updates are on.
func check(ctx context.Context, dataDir, current string, log *slog.Logger) *Release {
	if Load(dataDir).Off {
		return nil
	}
	r, err := Latest(ctx)
	if err != nil {
		log.Warn("update check", "err", err)
		return nil
	}
	newer := r.Newer(current)
	Edit(dataDir, func(s *State) {
		s.Checked, s.Available = time.Now(), ""
		if newer {
			s.Available = r.Version
		}
	})
	if !newer {
		return nil
	}
	log.Info("update found", "version", r.Version)
	return r
}

func installed(ctx context.Context, dataDir, current string, r *Release, log *slog.Logger) bool {
	dir, err := Dir()
	if err == nil {
		_, err = Install(ctx, dataDir, dir, r)
	}
	if err != nil {
		log.Warn("update", "version", r.Version, "err", err)
		return false
	}
	Edit(dataDir, func(s *State) { s.From, s.To, s.Updated, s.Available = current, r.Version, time.Now(), "" })
	log.Info("updated; restarting", "from", current, "to", r.Version)
	return true
}

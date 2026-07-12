package delivery

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Worker struct {
	Service      *Service
	Sender       Sender
	Hook         AcceptanceHook
	PollInterval time.Duration
}

func (w *Worker) Run(ctx context.Context) error {
	interval := w.PollInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := w.Service.ProcessOne(ctx, w.Sender, w.Hook)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("document delivery worker", "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			}
			continue
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

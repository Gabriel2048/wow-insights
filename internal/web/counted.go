package web

import (
	"context"

	"wowinsight/internal/warcraftlogs"
)

// counted is a logsClient that counts each call against the request it was
// made for, and notes where the hourly budget stood after it, so the access
// line can say what a page cost and what is left. It is the first decorator
// on the seam the cache (#2) will be the second on.
type counted struct {
	logsClient
}

// budgeted is what the real client offers beyond logsClient: the last budget
// snapshot. Asked for by type so the interface the handlers consume stays
// four methods and the test fake need not know about budgets.
type budgeted interface {
	Budget() (warcraftlogs.RateLimit, bool)
}

func (c counted) after(ctx context.Context) {
	if b, ok := c.logsClient.(budgeted); ok {
		if snapshot, known := b.Budget(); known {
			noteBudget(ctx, snapshot)
		}
	}
}

func (c counted) Report(ctx context.Context, code string) (*warcraftlogs.Report, error) {
	countCall(ctx)
	defer c.after(ctx)
	return c.logsClient.Report(ctx, code)
}

func (c counted) FightDetail(ctx context.Context, code string, fightID int) (*warcraftlogs.FightDetail, error) {
	countCall(ctx)
	defer c.after(ctx)
	return c.logsClient.FightDetail(ctx, code, fightID)
}

func (c counted) Timeline(ctx context.Context, code string, fight warcraftlogs.Fight, sourceID int) (*warcraftlogs.Timeline, error) {
	countCall(ctx)
	defer c.after(ctx)
	return c.logsClient.Timeline(ctx, code, fight, sourceID)
}

package web

import (
	"context"

	"wowinsight/internal/warcraftlogs"
)

// counted is a logsClient that counts each call against the request it was
// made for, so the access line can say what a page cost. It is the first
// decorator on the seam the cache (#2) will be the second on.
type counted struct {
	logsClient
}

func (c counted) Report(ctx context.Context, code string) (*warcraftlogs.Report, error) {
	countCall(ctx)
	return c.logsClient.Report(ctx, code)
}

func (c counted) FightDetail(ctx context.Context, code string, fightID int) (*warcraftlogs.FightDetail, error) {
	countCall(ctx)
	return c.logsClient.FightDetail(ctx, code, fightID)
}

func (c counted) Timeline(ctx context.Context, code string, fight warcraftlogs.Fight, sourceID int) (*warcraftlogs.Timeline, error) {
	countCall(ctx)
	return c.logsClient.Timeline(ctx, code, fight, sourceID)
}

package main

import (
	"context"

	"s3mail/awsx"
	"s3mail/web"
)

// suppressions haengt awsx an die Schnittstelle des Servers. Die beiden Typen
// getrennt zu halten kostet diese zwanzig Zeilen und spart dem web-Paket eine
// Abhaengigkeit auf das AWS-SDK - dadurch laufen seine Tests ohne Konto.
type suppressions struct{ l *awsx.Suppressions }

func (s suppressions) Block(ctx context.Context, a string) error   { return s.l.Block(ctx, a) }
func (s suppressions) Unblock(ctx context.Context, a string) error { return s.l.Unblock(ctx, a) }

func (s suppressions) List(ctx context.Context) ([]web.SuppressionEntry, error) {
	raw, err := s.l.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.SuppressionEntry, 0, len(raw))
	for _, e := range raw {
		out = append(out, web.SuppressionEntry{Address: e.Address, Reason: e.Reason, Since: e.Since})
	}
	return out, nil
}

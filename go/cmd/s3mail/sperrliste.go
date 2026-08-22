package main

import (
	"context"

	"s3mail/awsx"
	"s3mail/web"
)

// sperrliste haengt awsx an die Schnittstelle des Servers. Die beiden Typen
// getrennt zu halten kostet diese zwanzig Zeilen und spart dem web-Paket eine
// Abhaengigkeit auf das AWS-SDK - dadurch laufen seine Tests ohne Konto.
type sperrliste struct{ l *awsx.Sperrliste }

func (s sperrliste) Sperren(ctx context.Context, a string) error   { return s.l.Sperren(ctx, a) }
func (s sperrliste) Freigeben(ctx context.Context, a string) error { return s.l.Freigeben(ctx, a) }

func (s sperrliste) Lesen(ctx context.Context) ([]web.Sperreintrag, error) {
	roh, err := s.l.Lesen(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.Sperreintrag, 0, len(roh))
	for _, e := range roh {
		out = append(out, web.Sperreintrag{Adresse: e.Adresse, Grund: e.Grund, Seit: e.Seit})
	}
	return out, nil
}

package awsx

import (
	"context"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	v2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Suppressions ist die Unterdrueckungsliste des SES-Kontos: Adressen, an die SES
// nichts mehr zustellt.
//
// Sie gilt fuer das ganze Konto und nicht fuer ein Postfach. Wer hier eine
// Adresse eintraegt, sperrt sie fuer alle Kolleginnen und Kollegen und fuer
// jede andere Software, die ueber dasselbe Konto verschickt. Deshalb gibt es
// Freigeben() gleich mit: eine Sperre, die man nicht zurueecknehmen kann, ist
// bei einem Tippfehler eine Falle.
type Suppressions struct{ c *sesv2.Client }

func NewSuppressions(cfg aws.Config, endpunkt string) *Suppressions {
	return &Suppressions{c: sesv2.NewFromConfig(cfg, func(o *sesv2.Options) {
		if endpunkt != "" {
			o.BaseEndpoint = aws.String(endpunkt)
		}
	})}
}

// Entry ist eine gesperrte Adresse mit Grund und Zeitpunkt.
type Entry struct {
	Adresse string `json:"address"`
	Grund   string `json:"reason"`
	Seit    string `json:"since"`
}

// Block traegt eine Adresse ein. Grund ist COMPLAINT: der Eintrag entsteht,
// weil jemand darum gebeten hat, nicht mehr angeschrieben zu werden - genau
// das, was SES unter einer Beschwerde versteht. BOUNCE traegt SES selbst ein.
func (s *Suppressions) Block(ctx context.Context, adresse string) error {
	_, err := s.c.PutSuppressedDestination(ctx, &sesv2.PutSuppressedDestinationInput{
		EmailAddress: aws.String(adresse),
		Reason:       v2types.SuppressionListReasonComplaint,
	})
	return err
}

// Unblock nimmt eine Adresse wieder heraus.
func (s *Suppressions) Unblock(ctx context.Context, adresse string) error {
	_, err := s.c.DeleteSuppressedDestination(ctx, &sesv2.DeleteSuppressedDestinationInput{
		EmailAddress: aws.String(adresse),
	})
	return err
}

// List liefert die gesperrten Adressen, neueste zuerst.
func (s *Suppressions) List(ctx context.Context) ([]Entry, error) {
	out := []Entry{}
	var weiter *string
	for {
		resp, err := s.c.ListSuppressedDestinations(ctx,
			&sesv2.ListSuppressedDestinationsInput{NextToken: weiter, PageSize: aws.Int32(100)})
		if err != nil {
			return nil, err
		}
		for _, d := range resp.SuppressedDestinationSummaries {
			e := Entry{Adresse: aws.ToString(d.EmailAddress), Grund: string(d.Reason)}
			if d.LastUpdateTime != nil {
				e.Seit = d.LastUpdateTime.UTC().Format(time.RFC3339)
			}
			out = append(out, e)
		}
		// Der Zaehler begrenzt, was eine kaputte Antwort anrichten kann: ohne ihn
		// liefe die Schleife bei einem gleichbleibenden Token endlos.
		weiter = resp.NextToken
		if weiter == nil || len(out) > 5000 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seit > out[j].Seit })
	return out, nil
}

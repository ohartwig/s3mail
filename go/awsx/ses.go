package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"

	"s3mail/mailer"
)

// SES verschickt Antworten und Weiterleitungen.
type SES struct{ c *ses.Client }

func NewSES(cfg aws.Config, endpunkt string) *SES {
	return &SES{c: ses.NewFromConfig(cfg, func(o *ses.Options) {
		if endpunkt != "" {
			o.BaseEndpoint = aws.String(endpunkt)
		}
	})}
}

// Senden uebergibt die fertige Mail an SES und liefert deren Message-ID.
func (s *SES) Send(ctx context.Context, n mailer.Message) (string, error) {
	resp, err := s.c.SendRawEmail(ctx, &ses.SendRawEmailInput{
		Source:       aws.String(n.Absender),
		Destinations: n.Empfaenger,
		RawMessage:   &types.RawMessage{Data: n.Roh},
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(resp.MessageId), nil
}

// Identitaeten liefert die in SES verifizierten Absenderadressen. Fehlt das Recht,
// gibt es eine leere Liste statt eines Fehlers - der Assistent laesst dann eintippen.
func (s *SES) Identitaeten(ctx context.Context) []string {
	resp, err := s.c.ListIdentities(ctx, &ses.ListIdentitiesInput{
		IdentityType: types.IdentityTypeEmailAddress})
	if err != nil || len(resp.Identities) == 0 {
		return []string{} // nie nil - siehe Buckets()
	}
	attrs, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: resp.Identities})
	if err != nil {
		return resp.Identities
	}
	out := []string{}
	for _, id := range resp.Identities {
		if a, da := attrs.VerificationAttributes[id]; da &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			out = append(out, id)
		}
	}
	return out
}

// VerifizierteDomains liefert die freigeschalteten Domains.
//
// Getrennt von Identitaeten(), weil beides Verschiedenes bedeutet: eine
// verifizierte Adresse ist eine Adresse, eine verifizierte Domain erlaubt JEDE
// Adresse darunter. Wer nur Adress-Identitaeten abfragt - so wie diese Datei es
// zuerst tat - bekommt bei einem sauber ueber die Domain verifizierten Setup
// eine leere Liste und haelt sie fuer einen Fehler.
func (s *SES) VerifizierteDomains(ctx context.Context) []string {
	resp, err := s.c.ListIdentities(ctx, &ses.ListIdentitiesInput{
		IdentityType: types.IdentityTypeDomain})
	if err != nil || len(resp.Identities) == 0 {
		return []string{}
	}
	attrs, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: resp.Identities})
	if err != nil {
		return resp.Identities
	}
	out := []string{}
	for _, id := range resp.Identities {
		if a, da := attrs.VerificationAttributes[id]; da &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			out = append(out, id)
		}
	}
	return out
}

// Verifiziert sagt, ob Adresse oder Domain in SES freigeschaltet sind.
func (s *SES) Verifiziert(ctx context.Context, adresse, domain string) ([]string, error) {
	resp, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: []string{adresse, domain}})
	if err != nil {
		return nil, err
	}
	var gut []string
	for _, id := range []string{adresse, domain} {
		if a, da := resp.VerificationAttributes[id]; da &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			gut = append(gut, id)
		}
	}
	return gut, nil
}

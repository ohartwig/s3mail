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

func NewSES(cfg aws.Config, endpoint string) *SES {
	return &SES{c: ses.NewFromConfig(cfg, func(o *ses.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})}
}

// Senden uebergibt die fertige Mail an SES und liefert deren Message-ID.
func (s *SES) Send(ctx context.Context, n mailer.Message) (string, error) {
	resp, err := s.c.SendRawEmail(ctx, &ses.SendRawEmailInput{
		Source:       aws.String(n.From),
		Destinations: n.To,
		RawMessage:   &types.RawMessage{Data: n.Raw},
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(resp.MessageId), nil
}

// Identities liefert die in SES verifizierten Absenderadressen. Fehlt das Recht,
// gibt es eine leere Liste statt eines Fehlers - der Assistent laesst dann eintippen.
func (s *SES) Identities(ctx context.Context) []string {
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

// VerifiedDomains liefert die freigeschalteten Domains.
//
// Getrennt von Identitaeten(), weil beides Verschiedenes bedeutet: eine
// verifizierte Adresse ist eine Adresse, eine verifizierte Domain erlaubt JEDE
// Adresse darunter. Wer nur Adress-Identitaeten abfragt - so wie diese Datei es
// zuerst tat - bekommt bei einem sauber ueber die Domain verifizierten Setup
// eine leere Liste und haelt sie fuer einen Fehler.
func (s *SES) VerifiedDomains(ctx context.Context) []string {
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

// Verified sagt, ob Adresse oder Domain in SES freigeschaltet sind.
func (s *SES) Verified(ctx context.Context, address, domain string) ([]string, error) {
	resp, err := s.c.GetIdentityVerificationAttributes(ctx,
		&ses.GetIdentityVerificationAttributesInput{Identities: []string{address, domain}})
	if err != nil {
		return nil, err
	}
	var good []string
	for _, id := range []string{address, domain} {
		if a, da := resp.VerificationAttributes[id]; da &&
			a.VerificationStatus == types.VerificationStatusSuccess {
			good = append(good, id)
		}
	}
	return good, nil
}

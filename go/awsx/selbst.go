package awsx

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Fund ist, was s3mail ueber sich selbst herausgefunden hat.
type Fund struct {
	Benutzer string `json:"user"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
	Absender string `json:"from"`
	Region   string `json:"region"`
}

// Vollstaendig sagt, ob der Assistent damit ohne Rueckfrage weiterkommt.
func (f Fund) Vollstaendig() bool { return f.Bucket != "" && f.Prefix != "" }

// Erkunden liest die Einstellungen aus dem Zugang selbst, statt sie zu erfragen.
//
// Ein Postfach-Zugang traegt alles Noetige in seiner eigenen IAM-Policy: das
// Recht auf s3:GetObject nennt Bucket und Prefix, die Bedingung ses:FromAddress
// nennt die Absenderadresse. Das ist keine Rateerei - es ist genau die Angabe,
// an der AWS den Zugriff spaeter misst. Wer sie von Hand eintippt, kann sich nur
// unterscheiden, nicht verbessern.
//
// Fehlt das Recht, die eigene Policy zu lesen, kommt ein leerer Fund zurueck und
// der Assistent fragt wie bisher. Ein Fehler ist das nicht.
func Erkunden(ctx context.Context, cfg aws.Config) (Fund, error) {
	id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Fund{}, err
	}
	name := benutzerAusArn(aws.ToString(id.Arn))
	if name == "" {
		// Rolle, Root oder ein anderer Identitaetstyp - hat keine Benutzer-Policy.
		return Fund{}, nil
	}
	f := ausPolicies(policyTexte(ctx, iam.NewFromConfig(cfg), name))
	f.Benutzer = name
	if f.Bucket != "" {
		f.Region = BucketRegion(ctx, cfg, f.Bucket)
	}
	return f, nil
}

// benutzerAusArn zieht den Benutzernamen aus arn:aws:iam::123:user/pfad/name.
// Bei allem anderen (assumed-role, root, Dienst) kommt "" zurueck.
func benutzerAusArn(arn string) string {
	i := strings.Index(arn, ":user/")
	if i < 0 {
		return ""
	}
	rest := arn[i+len(":user/"):]
	if j := strings.LastIndex(rest, "/"); j >= 0 { // Pfad abschneiden
		rest = rest[j+1:]
	}
	return rest
}

// policyTexte holt die Policy-Dokumente des Benutzers - erst die eingebetteten,
// dann die angehaengten. Jeder Schritt darf scheitern, ohne den Rest mitzureissen:
// die meisten Zugaenge duerfen nur einen Teil davon lesen, manche gar nichts.
func policyTexte(ctx context.Context, c *iam.Client, benutzer string) []string {
	var out []string
	if l, err := c.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{UserName: &benutzer}); err == nil {
		for _, n := range l.PolicyNames {
			p, err := c.GetUserPolicy(ctx, &iam.GetUserPolicyInput{UserName: &benutzer, PolicyName: &n})
			if err == nil {
				out = append(out, aws.ToString(p.PolicyDocument))
			}
		}
	}
	if l, err := c.ListAttachedUserPolicies(ctx,
		&iam.ListAttachedUserPoliciesInput{UserName: &benutzer}); err == nil {
		for _, a := range l.AttachedPolicies {
			m, err := c.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: a.PolicyArn})
			if err != nil {
				continue
			}
			v, err := c.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
				PolicyArn: a.PolicyArn, VersionId: m.Policy.DefaultVersionId})
			if err == nil {
				out = append(out, aws.ToString(v.PolicyVersion.Document))
			}
		}
	}
	return out
}

// BucketRegion sagt, in welcher Region ein Bucket steht.
//
// S3 verraet das auch dem, der den Bucket nicht anfassen darf: der Header
// x-amz-bucket-region liegt selbst einer 403- oder 301-Antwort bei. Deshalb
// genuegt hier ein HeadBucket, obwohl ein Postfach-Zugang den gar nicht
// ausfuehren darf - wir brauchen nicht die Antwort, sondern den Briefkopf.
func BucketRegion(ctx context.Context, cfg aws.Config, bucket string) string {
	out, err := s3.NewFromConfig(cfg).HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket})
	if err == nil {
		if r := aws.ToString(out.BucketRegion); r != "" {
			return r
		}
		return cfg.Region
	}
	var antwort interface{ HTTPResponse() *http.Response }
	if errors.As(err, &antwort) {
		if r := antwort.HTTPResponse().Header.Get("x-amz-bucket-region"); r != "" {
			return r
		}
	}
	return ""
}

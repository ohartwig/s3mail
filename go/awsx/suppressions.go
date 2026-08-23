package awsx

import (
	"context"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	v2types "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Suppressions is the SES account's suppression list: addresses SES no longer
// accepts mail for.
//
// It belongs to the whole account, not to one mailbox. Whoever enters an address
// here blocks it for every colleague and for every other piece of software that
// sends over the same account. That is why Unblock() comes with it: a block
// nobody can take back is a trap on a typo.
type Suppressions struct{ c *sesv2.Client }

func NewSuppressions(cfg aws.Config, endpoint string) *Suppressions {
	return &Suppressions{c: sesv2.NewFromConfig(cfg, func(o *sesv2.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})}
}

// Entry is a blocked address with reason and time.
type Entry struct {
	Address string `json:"address"`
	Reason  string `json:"reason"`
	Since   string `json:"since"`
}

// Block enters an address. The reason is COMPLAINT: the entry comes into being
// because somebody asked not to be written to any more - exactly what SES means
// by a complaint. BOUNCE is what SES enters itself.
func (s *Suppressions) Block(ctx context.Context, address string) error {
	_, err := s.c.PutSuppressedDestination(ctx, &sesv2.PutSuppressedDestinationInput{
		EmailAddress: aws.String(address),
		Reason:       v2types.SuppressionListReasonComplaint,
	})
	return err
}

// Unblock takes an address out again.
func (s *Suppressions) Unblock(ctx context.Context, address string) error {
	_, err := s.c.DeleteSuppressedDestination(ctx, &sesv2.DeleteSuppressedDestinationInput{
		EmailAddress: aws.String(address),
	})
	return err
}

// List returns the blocked addresses, newest first.
func (s *Suppressions) List(ctx context.Context) ([]Entry, error) {
	out := []Entry{}
	var more *string
	for {
		resp, err := s.c.ListSuppressedDestinations(ctx,
			&sesv2.ListSuppressedDestinationsInput{NextToken: more, PageSize: aws.Int32(100)})
		if err != nil {
			return nil, err
		}
		for _, d := range resp.SuppressedDestinationSummaries {
			e := Entry{Address: aws.ToString(d.EmailAddress), Reason: string(d.Reason)}
			if d.LastUpdateTime != nil {
				e.Since = d.LastUpdateTime.UTC().Format(time.RFC3339)
			}
			out = append(out, e)
		}
		// The counter limits what a broken answer can do: without it the loop would
		// run forever on a token that never changes.
		more = resp.NextToken
		if more == nil || len(out) > 5000 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Since > out[j].Since })
	return out, nil
}

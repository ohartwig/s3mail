package awsx

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// LifecycleRegelID ist die Kennung unserer Papierkorb-Regel. Alles andere im
// Bucket bleibt unangetastet.
const LifecycleRegelID = "s3mail-trash"

// Buckets listet alle Buckets des Kontos. Fehlt s3:ListAllMyBuckets, kommt eine
// leere Liste zurueck - der Assistent laesst den Namen dann eintippen.
func (a *S3) Buckets(ctx context.Context) ([]string, error) {
	resp, err := a.c.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "AccessDenied" ||
			api.ErrorCode() == "AccessDeniedException") {
			// Leeres Slice, nicht nil: ein nil-Slice wird zu JSON `null`, und die
			// Oberflaeche ruft darauf .map() auf. Fehlendes s3:ListAllMyBuckets ist
			// der Normalfall - die Postfach-Benutzer bekommen es absichtlich nicht.
			return []string{}, nil
		}
		return []string{}, err
	}
	out := make([]string, 0, len(resp.Buckets))
	for _, b := range resp.Buckets {
		out = append(out, aws.ToString(b.Name))
	}
	return out, nil
}

// LifecycleTage liest, nach wievielen Tagen der Papierkorb geleert wird. 0 heisst
// "keine Regel von uns".
func (a *S3) LifecycleTage(ctx context.Context, bucket string) int {
	resp, err := a.c.GetBucketLifecycleConfiguration(ctx,
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)})
	if err != nil {
		return 0
	}
	for _, r := range resp.Rules {
		if aws.ToString(r.ID) == LifecycleRegelID && r.Expiration != nil {
			return int(aws.ToInt32(r.Expiration.Days))
		}
	}
	return 0
}

// LifecycleSetzen legt die Papierkorb-Regel an, aendert sie oder nimmt sie mit
// tage=0 wieder heraus. Fremde Regeln im Bucket bleiben stehen.
func (a *S3) LifecycleSetzen(ctx context.Context, bucket, prefix string, tage int) (string, error) {
	var bestehend []types.LifecycleRule
	if resp, err := a.c.GetBucketLifecycleConfiguration(ctx,
		&s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(bucket)}); err == nil {
		for _, r := range resp.Rules {
			if aws.ToString(r.ID) != LifecycleRegelID {
				bestehend = append(bestehend, r)
			}
		}
	}
	if tage > 0 {
		bestehend = append(bestehend, types.LifecycleRule{
			ID:         aws.String(LifecycleRegelID),
			Status:     types.ExpirationStatusEnabled,
			Filter:     &types.LifecycleRuleFilter{Prefix: aws.String(prefix + "trash/")},
			Expiration: &types.LifecycleExpiration{Days: aws.Int32(int32(tage))},
		})
	}
	if len(bestehend) == 0 {
		if _, err := a.c.DeleteBucketLifecycle(ctx,
			&s3.DeleteBucketLifecycleInput{Bucket: aws.String(bucket)}); err != nil {
			return "", err
		}
		return "Papierkorb-Automatik entfernt.", nil
	}
	_, err := a.c.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String(bucket),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: bestehend},
	})
	if err != nil {
		return "", err
	}
	if tage == 0 {
		return "Papierkorb-Automatik entfernt.", nil
	}
	return fmt.Sprintf("Papierkorb wird nach %d Tagen automatisch geleert.", tage), nil
}

package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"s3mail/store"
)

// KMS setzt store.KMS auf das AWS-SDK um.
type KMS struct{ c *kms.Client }

func NewKMS(cfg aws.Config, endpoint string) *KMS {
	return &KMS{c: kms.NewFromConfig(cfg, func(o *kms.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})}
}

// Decrypt entpackt den Datenschluessel. Der Encryption Context muss mit - ohne ihn
// lehnt KMS ab, und die Fehlermeldung sagt nicht, woran es lag.
func (k *KMS) Decrypt(ciphertext []byte, kontext map[string]string) ([]byte, error) {
	resp, err := k.c.Decrypt(context.Background(), &kms.DecryptInput{
		CiphertextBlob:    ciphertext,
		EncryptionContext: kontext,
	})
	if err != nil {
		return nil, err
	}
	return resp.Plaintext, nil
}

var _ store.KMS = (*KMS)(nil)

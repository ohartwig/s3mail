// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"git.ole-hartwig.eu/development/s3mail/s3mail/store"
)

// KMS maps store.KMS onto the AWS SDK.
type KMS struct{ c *kms.Client }

func NewKMS(cfg aws.Config, endpoint string) *KMS {
	return &KMS{c: kms.NewFromConfig(cfg, func(o *kms.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})}
}

// Decrypt unwraps the data key. The encryption context has to come along -
// without it KMS refuses, and the error message does not say why.
func (k *KMS) Decrypt(ciphertext []byte, encContext map[string]string) ([]byte, error) {
	resp, err := k.c.Decrypt(context.Background(), &kms.DecryptInput{
		CiphertextBlob:    ciphertext,
		EncryptionContext: encContext,
	})
	if err != nil {
		return nil, err
	}
	return resp.Plaintext, nil
}

var _ store.KMS = (*KMS)(nil)

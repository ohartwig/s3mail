// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package awsx

import (
	"encoding/json"
	"testing"
)

// sesNotification is an SES receipt notification as the S3 action sends it,
// cut to the fields that matter here.
const sesNotification = `{"notificationType":"Received",
 "receipt":{"recipients":["k.hartwig@example.org"],
  "action":{"type":"S3","topicArn":"arn:aws:sns:eu-north-1:123456789012:t",
   "bucketName":"mail-bucket","objectKeyPrefix":"mail/k-hartwig/",
   "objectKey":"mail/k-hartwig/o3vrnil0e2ic28trm7dfhrc2v0clambda4nbp0g1"}},
 "mail":{"messageId":"o3vrnil0e2ic28trm7dfhrc2v0clambda4nbp0g1"}}`

// TestTheNotificationNamesTheKey - the key is what saves the listing. Without
// it every new mail would cost a look at the inbox.
func TestTheNotificationNamesTheKey(t *testing.T) {
	want := "mail/k-hartwig/o3vrnil0e2ic28trm7dfhrc2v0clambda4nbp0g1"
	if got := ObjectKey(sesNotification); got != want {
		t.Errorf("raw delivery: %q, expected %q", got, want)
	}
	env, _ := json.Marshal(map[string]string{"Type": "Notification", "Message": sesNotification})
	if got := ObjectKey(string(env)); got != want {
		t.Errorf("inside an SNS envelope: %q, expected %q", got, want)
	}
}

// TestAnUnreadableNotificationNamesNothing - "" makes the caller look at the
// inbox instead; a guessed key would make it look at the wrong object.
func TestAnUnreadableNotificationNamesNothing(t *testing.T) {
	for _, body := range []string{"", "not json", `{"receipt":{"action":{"type":"Lambda","objectKey":"x"}}}`,
		`{"Type":"Notification","Message":"also not json"}`} {
		if got := ObjectKey(body); got != "" {
			t.Errorf("%q gave %q", body, got)
		}
	}
}

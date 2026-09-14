# Setting up a mailbox

s3mail reads mail that Amazon SES writes into an S3 bucket you own. This
template creates that bucket and an IAM user narrow enough to be the only thing
that reads it — the two pieces s3mail cannot make for itself.

Everything else it works out on its own: bucket, prefix and sender all come out
of the IAM policy of the access it runs as, so the setup asks for a key and
nothing more.

## Before you start

**A domain SES receives for.** Verify it in the SES console first. This template
cannot do it, because verification needs DNS records only you can add.

**A region where SES can receive.** Not every region can — SES sending is
everywhere, receiving is not. Check the SES documentation for the current list
and create the stack there.

## Launch it

```bash
aws cloudformation create-stack \
  --stack-name s3mail \
  --template-body file://s3mail.json \
  --capabilities CAPABILITY_NAMED_IAM \
  --parameters \
      ParameterKey=MailDomain,ParameterValue=example.org \
      ParameterKey=MailboxLocalPart,ParameterValue=ole \
      ParameterKey=MailBucket,ParameterValue=your-unique-bucket-name
```

`CAPABILITY_NAMED_IAM` because the stack creates IAM policies under names of its
own. Naming them is what lets s3mail find the permissions boundary for pairing a
phone.

## Two things the stack cannot finish

**Activate the rule set.** CloudFormation can create a receipt rule set but not
make it the active one — there is no resource for it, only an API call:

```bash
aws ses set-active-receipt-rule-set --rule-set-name s3mail-ole
```

**Mint a key.** The template creates no access key, on purpose: a key in a stack
output is readable by anyone who can read the stack, forever, and it is the one
secret in this whole arrangement.

```bash
aws iam create-access-key --user-name s3mail-ole
```

Then start s3mail, enter that key, and let it work the rest out.

## What it creates

| | |
|---|---|
| **Bucket** | versioned, encrypted, public access blocked. `DeletionPolicy: Retain` — deleting the stack must not delete your mail. |
| **Bucket policy** | SES may write. Everyone except this mailbox and its paired devices is **denied** reading and listing — an explicit Deny beats every Allow, `AdministratorAccess` included. |
| **IAM user** | what s3mail runs as. Its policy is also its configuration. |
| **Device boundary** | the ceiling a paired phone can never exceed, whatever policy is written for it. |
| **Device admin** | lets the mailbox pair its own phones — and only with that boundary attached. |
| **SES receipt rule** | writes incoming mail for this address into the bucket. |

## What it deliberately leaves out

**Push.** Notifications need an SNS platform application holding an APNs signing
key, which cannot live in a template. Without it s3mail checks for new mail on a
timer, which is what it did for a long time.

**More than one mailbox.** Launch the stack again with a different local part.
Each gets its own prefix, its own boundary and its own devices.

## Why this is checked by a test

The template describes a contract — which bucket, which prefix, which sender,
which boundary — and so does the Terraform that runs the author's own mailboxes.
Neither is the authority. `awsx.fromPolicies` is: whatever s3mail can read out
of an access's own policy is what a setup has to produce.

`awsx/deploytemplate_test.go` resolves this template the way a deployment would
and runs the result through that function. Without it the two would drift, and
the drift would be invisible — the template would keep deploying and the stack
would keep succeeding, while a stranger ended up with a mailbox s3mail could not
make sense of.

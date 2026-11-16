// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"

	"github.com/ohartwig/s3mail/awsx"
	"github.com/ohartwig/s3mail/config"
	"github.com/ohartwig/s3mail/mcp"
	"github.com/ohartwig/s3mail/store"
)

// serveMCP hands the mailboxes to a model over stdin and stdout.
//
// Deliberately without the sender: the MCP server has no way to send, and it
// cannot grow one by accident here either. What it can do is write a draft,
// which a human then sends from s3mail.
func serveMCP(ctx context.Context, k config.Config, readOnly bool, reconcileMinutes int,
	cacheDir string, cacheKey []byte) error {
	if len(k.Accounts) == 0 {
		return errors.New("s3mail is not set up yet - start it once without --mcp")
	}
	mcp.Version = version

	accounts := make([]mcp.Account, 0, len(k.Accounts))
	var firstErr error
	for _, a := range k.Accounts {
		cfg, err := awsx.Session(ctx, a.Profile, a.Region)
		if err == nil {
			err = awsx.CheckAccess(ctx, cfg)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		mb := store.NewMailbox(ctx, awsx.NewS3Client(cfg, ""), awsx.NewKMS(cfg, ""),
			a.Bucket, a.Prefix, cacheDir, cacheKey, k.AllowDelete)
		mb.ReconcileEvery = reconcileEvery(reconcileMinutes)
		// Once at the start, so the first search does not answer out of an empty
		// index. After that the model works on what it has; it is not a window
		// somebody watches.
		//
		// Sync, not Refresh: an MCP server is started per session, and with a
		// cache younger than the reconcile interval the journal and the inbox
		// are all that can have changed. Listing the whole mailbox per session
		// is the bill this is here to avoid.
		_, _ = mb.Sync(ctx, store.SyncOpts{Inbox: true})
		accounts = append(accounts, mcp.Account{
			ID: a.ID(), Name: a.Name(), Mailbox: mb, From: a.From})
	}
	if len(accounts) == 0 {
		return firstErr
	}
	srv := mcp.New(ctx, accounts)
	srv.ReadOnly = readOnly
	return srv.Serve(os.Stdin, os.Stdout)
}

package horizon

import (
	"context"
	"fmt"
	"net/url"
)

// This file serves the attester's issuer watcher (internal/attester). It is
// kept apart from client.go on purpose: nothing here feeds a severity, which is
// what makes client.go a maintainer-owned path in scripts/merge-gate.sh.

// Operation is the subset of Horizon's operation record the issuer watcher
// reads. Field names are the Horizon wire names; set_flags/clear_flags are the
// protocol's AccountFlags bit values (1 auth_required, 2 auth_revocable,
// 4 auth_immutable, 8 auth_clawback_enabled), present only on set_options.
type Operation struct {
	ID                    string `json:"id"`
	PagingToken           string `json:"paging_token"`
	Type                  string `json:"type"`
	TransactionSuccessful bool   `json:"transaction_successful"`
	TransactionHash       string `json:"transaction_hash"`
	CreatedAt             string `json:"created_at"`
	SourceAccount         string `json:"source_account"`
	SetFlags              []int  `json:"set_flags"`
	ClearFlags            []int  `json:"clear_flags"`
}

// Order is the direction an operations page is returned in.
type Order string

const (
	// Ascending returns the oldest operations after the cursor first.
	Ascending Order = "asc"
	// Descending returns the newest operations first.
	Descending Order = "desc"
)

// Operations returns one page of the operations whose source or participant
// is account, after cursor (empty means from the start of the history in
// order's direction), at most limit records.
//
// No `type=` filter is sent: the public Horizon instances silently ignore it
// (docs/freshness.md, verified 2026-09-24), so callers filter client-side on
// Operation.Type rather than trusting a server-side filter.
func (c *Client) Operations(ctx context.Context, account, cursor string, order Order, limit int) ([]Operation, error) {
	q := url.Values{}
	q.Set("order", string(order))
	q.Set("limit", fmt.Sprint(limit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var page struct {
		Embedded struct {
			Records []Operation `json:"records"`
		} `json:"_embedded"`
	}
	if err := c.get(ctx, "/accounts/"+url.PathEscape(account)+"/operations?"+q.Encode(), &page); err != nil {
		return nil, err
	}
	return page.Embedded.Records, nil
}

package attester

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/use-assay/assay/internal/horizon"
)

// The protocol's AccountFlags bits, as set_options carries them.
const (
	flagAuthRequired        = 1
	flagAuthRevocable       = 2
	flagAuthImmutable       = 4
	flagAuthClawbackEnabled = 8
)

// operationsPageSize is how many operations one poll request asks for. A
// backlog larger than this is drained page by page within the same poll.
const operationsPageSize = 200

// FlagTransition is one observed change in an attested issuer's authorization
// flags: the capability transition the writer design's --watch reacts to.
// Flag names are Horizon's wire names, the vocabulary the scan reports in.
type FlagTransition struct {
	Issuer      string        `json:"issuer"`
	Assets      []string      `json:"assets"`
	From        horizon.Flags `json:"from"`
	To          horizon.Flags `json:"to"`
	Added       []string      `json:"added,omitempty"`
	Removed     []string      `json:"removed,omitempty"`
	OperationID string        `json:"operation_id"`
	TxHash      string        `json:"transaction_hash"`
	At          string        `json:"at"`
}

// ApplyOperation returns the flags an issuer has after op, and whether they
// changed.
//
// Only successful set_options operations sourced by the issuer itself can
// move its flags. A set_options that sets a flag the issuer already has, or
// clears one it does not, is a no-op: Horizon still records it (and emits an
// account_flags_updated effect for it, docs/freshness.md), so value changes
// are computed here rather than counted from records.
func ApplyOperation(prev horizon.Flags, issuer string, op horizon.Operation) (horizon.Flags, bool) {
	if op.Type != "set_options" || !op.TransactionSuccessful {
		return prev, false
	}
	if op.SourceAccount != "" && op.SourceAccount != issuer {
		return prev, false
	}
	next := prev
	set := func(bit int, v bool) {
		switch bit {
		case flagAuthRequired:
			next.AuthRequired = v
		case flagAuthRevocable:
			next.AuthRevocable = v
		case flagAuthImmutable:
			next.AuthImmutable = v
		case flagAuthClawbackEnabled:
			next.AuthClawbackEnabled = v
		}
	}
	for _, b := range op.ClearFlags {
		set(b, false)
	}
	for _, b := range op.SetFlags {
		set(b, true)
	}
	return next, next != prev
}

// flagNames maps each flag's wire name to whether f has it set.
func flagNames(f horizon.Flags) map[string]bool {
	return map[string]bool{
		"auth_required":         f.AuthRequired,
		"auth_revocable":        f.AuthRevocable,
		"auth_immutable":        f.AuthImmutable,
		"auth_clawback_enabled": f.AuthClawbackEnabled,
	}
}

func flagDelta(from, to horizon.Flags) (added, removed []string) {
	a, b := flagNames(from), flagNames(to)
	for name := range b {
		switch {
		case b[name] && !a[name]:
			added = append(added, name)
		case !b[name] && a[name]:
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// HorizonReader is the part of horizon.Client the watcher uses.
type HorizonReader interface {
	Account(ctx context.Context, id string) (*horizon.Account, error)
	Operations(ctx context.Context, account, cursor string, order horizon.Order, limit int) ([]horizon.Operation, error)
}

type issuerState struct {
	issuer string
	assets []string
	flags  horizon.Flags
	cursor string
}

// Watcher polls each attested issuer's operations and reports authorization
// flag changes. It is read-only: it detects and emits, and nothing more. It
// does not re-attest, because nothing in this package can sign.
type Watcher struct {
	h       HorizonReader
	issuers []*issuerState
}

// NewWatcher groups the configured assets by issuer (one issuer can back
// several attested assets) and prepares one watch per issuer.
func NewWatcher(cfg *Config, h HorizonReader) *Watcher {
	byIssuer := map[string]*issuerState{}
	var order []string
	for _, a := range cfg.ParsedAssets() {
		st, ok := byIssuer[a.Issuer]
		if !ok {
			st = &issuerState{issuer: a.Issuer}
			byIssuer[a.Issuer] = st
			order = append(order, a.Issuer)
		}
		st.assets = append(st.assets, a.String())
	}
	w := &Watcher{h: h}
	for _, id := range order {
		w.issuers = append(w.issuers, byIssuer[id])
	}
	return w
}

// Init records each issuer's current flags and the newest operation's cursor,
// so polling reports only changes made after this point. The cursor is read
// before the flags: an operation landing in between is then already in the
// baseline flags and replays as a no-op, rather than being missed.
func (w *Watcher) Init(ctx context.Context) error {
	for _, st := range w.issuers {
		latest, err := w.h.Operations(ctx, st.issuer, "", horizon.Descending, 1)
		if err != nil {
			return fmt.Errorf("watch %s: read latest operation: %w", st.issuer, err)
		}
		if len(latest) > 0 {
			st.cursor = latest[0].PagingToken
		}
		acct, err := w.h.Account(ctx, st.issuer)
		if err != nil {
			return fmt.Errorf("watch %s: read flags: %w", st.issuer, err)
		}
		st.flags = acct.Flags
	}
	return nil
}

// Poll reads every operation each issuer gained since the last poll, filters
// them client-side, and returns the flag transitions they caused, oldest
// first. An issuer whose fetch fails keeps its cursor, so the next poll
// retries from the same point instead of skipping operations.
func (w *Watcher) Poll(ctx context.Context) ([]FlagTransition, error) {
	var out []FlagTransition
	var firstErr error
	for _, st := range w.issuers {
		ts, err := w.pollIssuer(ctx, st)
		out = append(out, ts...)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

func (w *Watcher) pollIssuer(ctx context.Context, st *issuerState) ([]FlagTransition, error) {
	var out []FlagTransition
	for {
		ops, err := w.h.Operations(ctx, st.issuer, st.cursor, horizon.Ascending, operationsPageSize)
		if err != nil {
			return out, fmt.Errorf("watch %s: %w", st.issuer, err)
		}
		for _, op := range ops {
			next, changed := ApplyOperation(st.flags, st.issuer, op)
			if changed {
				added, removed := flagDelta(st.flags, next)
				out = append(out, FlagTransition{
					Issuer:      st.issuer,
					Assets:      append([]string(nil), st.assets...),
					From:        st.flags,
					To:          next,
					Added:       added,
					Removed:     removed,
					OperationID: op.ID,
					TxHash:      op.TransactionHash,
					At:          op.CreatedAt,
				})
				st.flags = next
			}
			st.cursor = op.PagingToken
		}
		if len(ops) < operationsPageSize {
			return out, nil
		}
	}
}

// Run polls every interval until ctx is done, calling emit for each
// transition and onErr for each failed poll. A failed poll is reported, not
// fatal: a Horizon blip must not stop the watch.
func (w *Watcher) Run(ctx context.Context, interval time.Duration, emit func(FlagTransition), onErr func(error)) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("watch stopped: %w", ctx.Err())
		case <-t.C:
			ts, err := w.Poll(ctx)
			for _, tr := range ts {
				emit(tr)
			}
			if err != nil && ctx.Err() == nil {
				onErr(err)
			}
		}
	}
}

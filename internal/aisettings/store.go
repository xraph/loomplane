package aisettings

import (
	"context"
	"errors"
)

// Errors a Store returns. Test with errors.Is. ErrOverBudget and
// ErrUnderAllocated come wrapped with the amounts involved.
var (
	ErrNotFound       = errors.New("not found in the workspace AI settings")
	ErrStale          = errors.New("the workspace AI settings changed since they were read")
	ErrOverBudget     = errors.New("over the workspace AI budget")
	ErrUnderAllocated = errors.New("the workspace AI budget is below what is already allocated")
	// ErrBusy is a Hugging Face token lock a caller waited too long for.
	ErrBusy = errors.New("the workspace's Hugging Face token is busy")
)

// Store keeps workspace AI settings, provider key metadata and budget
// allocations. Money is a decimal string with two places ("100.00"), never
// a float.
type Store interface {
	// Settings returns the workspace's settings. A workspace with no row yet
	// gets the defaults: budget "0.00", local embeddings and Revision 0.
	Settings(ctx context.Context, orgID string) (*Settings, error)
	// SaveSettings writes s.Embeddings and s.UpdatedBy when the stored
	// revision is expectRevision (0 for a workspace with no row yet), and
	// ErrStale otherwise. It leaves the budget alone: that changes only
	// through SetBudget. On success s holds the stored row.
	SaveSettings(ctx context.Context, s *Settings, expectRevision int64) error

	// ProviderKeys lists the workspace's provider keys by name.
	ProviderKeys(ctx context.Context, orgID string) ([]*ProviderKey, error)
	// ProviderKey returns one key by name, or ErrNotFound.
	ProviderKey(ctx context.Context, orgID, name string) (*ProviderKey, error)
	// SaveProviderKey inserts k, or updates the key with its org and name.
	// An update keeps the stored ID, CreatedBy and CreatedAt and bumps
	// Revision. On success k holds the stored row.
	SaveProviderKey(ctx context.Context, k *ProviderKey) error
	// CreateProviderKey inserts k, and only inserts: when the workspace
	// already has a key with k's name it changes nothing and returns
	// ErrStale. An empty k.ID gets a new one. On success k holds the stored
	// row.
	CreateProviderKey(ctx context.Context, k *ProviderKey) error
	// DeleteProviderKey deletes one key by name, or returns ErrNotFound.
	DeleteProviderKey(ctx context.Context, orgID, name string) error

	// Allocations lists the workspace's budget allocations by scope.
	Allocations(ctx context.Context, orgID string) ([]*Allocation, error)
	// SetAllocation writes one allocation inside a transaction that locks the
	// settings row, and refuses (ErrOverBudget, with the amount left) when the
	// new total would pass the workspace budget.
	SetAllocation(ctx context.Context, orgID string, a *Allocation) error
	// DeleteAllocation deletes one allocation by scope. Deleting one that
	// isn't there is not an error: its owner may be cleaning up twice.
	DeleteAllocation(ctx context.Context, orgID, scope string) error
	// SetBudget locks the same row and refuses ErrUnderAllocated when the
	// allocations already sum above the new budget. A change bumps Revision.
	SetBudget(ctx context.Context, orgID, budgetUSD, by string) (*Settings, error)

	// SetHuggingFaceToken records that the workspace has a Hugging Face
	// token, with its hint and account, set at now. It bumps the token's
	// revision, not the settings', and returns what it stored.
	SetHuggingFaceToken(ctx context.Context, orgID, hint, account string) (*HuggingFaceToken, error)
	// ClearHuggingFaceToken records that the workspace has no Hugging Face
	// token, bumping the token's revision. Clearing when there's none is not
	// an error.
	ClearHuggingFaceToken(ctx context.Context, orgID string) error
	// LockHuggingFaceToken runs fn while it holds the workspace's Hugging
	// Face token lock, and returns fn's error. One workspace's holders run
	// one at a time, across every replica; other workspaces don't wait. fn
	// may use the store, which doesn't take the lock itself. A caller that
	// waits too long for the lock gets ErrBusy, and fn doesn't run.
	LockHuggingFaceToken(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

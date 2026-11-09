//go:build cloud

// Package billing is Loomplane Cloud's billing: its config, the ledger it
// bills through (with Stripe as the payment provider), and the tables it
// keeps in Loomplane's database. It exists only in binaries built with
// -tags cloud.
//
// It never imports extensions/identity: identity's cloud edition imports
// this package to build the ledger authsome needs.
package billing

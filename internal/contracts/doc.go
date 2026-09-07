// Package contracts holds the cross-context integration pins: the assertions
// that one bounded context's copy of another's integration contract still
// equals the original. It exists because there is nowhere else left to put
// them. Those assertions used to live in cmd/api — since split away and gone
// — which imported every context because it served every context's routes.
// Now that the HTTP tier is split, no composition root imports two contexts,
// and a drift between the two copies would be silent everywhere.
//
// ddd-architecture grants this package the cross-context import it forbids
// everywhere else, and the permission is conditional: this package declares
// nothing outside its _test.go files apart from this comment, and no other
// package in the repository imports it. Both are asserted by a test here.
// The first is what makes it undependable — a package exporting nothing
// cannot be imported for a symbol — and the second closes the blank import
// the first still permits. Together they keep it from becoming the shared
// domain package the dependency rules exist to prevent.
package contracts

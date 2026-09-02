package domain

import "context"

// JobLeaseStore is the liveness signal behind recovery: a worker running an
// extraction keeps a short-lived, periodically renewed lease on the job, and
// the absence of that lease is what tells the recovery sweep the worker is
// gone. It is deliberately not a lock — nothing consults it before claiming a
// job. Exclusivity comes from the conditional claim and from the fenced
// terminal write; this port only answers "is someone still working on it".
//
// Every method carries the lease epoch, the query included. The epoch is a
// sufficient holder identity because only a requeue changes it and only a
// requeue can produce a second holder, so "the lease at epoch N" names
// exactly one run of the job.
//
// Held in particular must not degrade to asking whether the key exists. If
// the current holder never managed to acquire — acquisition fails open — and
// a superseded claimant's late acquire found the key absent, the store holds
// an older epoch; a job-ID-only query would then report the job as held while
// its actual holder is fenced and its row is unrecoverable.
type JobLeaseStore interface {
	// Acquire takes the lease for jobID at epoch, replacing a stored value
	// that is absent or older. A stored value naming a newer epoch means
	// this caller has already been superseded: acquisition is refused and
	// reported as acquired = false, which is an ordinary outcome, not an
	// error.
	Acquire(ctx context.Context, jobID VideoJobID, epoch int64) (acquired bool, err error)
	// Renew extends the lease, conditional on the stored value still naming
	// epoch. A false return means the caller has been taken over and should
	// stop renewing.
	Renew(ctx context.Context, jobID VideoJobID, epoch int64) (renewed bool, err error)
	// Release drops the lease, conditional on the stored value still naming
	// epoch, so a superseded holder cannot release its successor's.
	Release(ctx context.Context, jobID VideoJobID, epoch int64) error
	// Held reports whether the job is leased at exactly epoch. A stored
	// value naming any other epoch is not this generation's lease and is
	// reported as not held.
	//
	// It returns an error rather than folding one into held = false: "cannot
	// reach the store" is not evidence a lease expired, and the caller's
	// fail-closed posture depends on telling the two apart.
	Held(ctx context.Context, jobID VideoJobID, epoch int64) (held bool, err error)
}

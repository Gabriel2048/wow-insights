package coach

import "errors"

// The sentinels a caller decides on. Flat, like the client's: errors.Is walks
// a wrap chain and not a type tree, so there is no base error to catch and
// nothing would be gained by one.
//
// Every one of these is a reason the page shows the analysis's own words. None
// of them is a reason to fail a page — the findings were computed before any
// of this was attempted and are true whatever happened here.
var (
	// ErrModelUnavailable: the model could not be reached, or answered with
	// something other than an answer.
	ErrModelUnavailable = errors.New("coach: the model is unavailable")
	// ErrModelBusy: rate limited or overloaded. Worth trying again; the same
	// request later may well work.
	ErrModelBusy = errors.New("coach: the model is busy")
	// ErrBadKey: the API key is missing, wrong or revoked. A deployment
	// fault, not an outage, and it will not fix itself.
	ErrBadKey = errors.New("coach: the model rejected this server's key")
	// ErrNoCredit: the account behind the key has no money on it. It is
	// separate from ErrBadKey because the remedy is completely different —
	// nobody fixes this by rotating a credential — and separate from an
	// outage because it will not fix itself.
	ErrNoCredit = errors.New("coach: the account behind this server's key has no credit")
	// ErrDeclined: the model refused the request. Nothing here is worth
	// refusing, so this means something is wrong with what was sent.
	ErrDeclined = errors.New("coach: the model declined to answer")
	// ErrUntrustworthy: the reply did not survive validation — it named
	// something that is not in the pull, or stated a number that is not in
	// the evidence, or did not answer about the findings it was given. The
	// page shows the deterministic wording and says the prose was dropped.
	ErrUntrustworthy = errors.New("coach: the model's wording did not check out")
	// ErrWouldLeak: the outbound body contains something from this report
	// that must not leave the process. Nothing is sent. This is a defect in
	// this package, not a condition to handle, and it is loud on purpose.
	ErrWouldLeak = errors.New("coach: refusing to send a body carrying report identity")
)

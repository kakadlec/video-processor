package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"video-processor/internal/notification/domain"
)

// signaturePrefix names the algorithm on the wire, so a later one can be
// introduced without a receiver having to guess which it is looking at.
const signaturePrefix = "sha256="

// sign computes the signature a receiver verifies, as HMAC-SHA256 over
// "<timestamp>.<body>" rendered hex behind signaturePrefix.
//
// The timestamp is bound into the signed value rather than only carried
// beside it: signing the body alone would leave a captured request
// replayable forever, and a receiver that bounds the timestamp's age can
// only rely on that bound if the attacker cannot move the timestamp.
//
// This is the only function that reveals a secret read back out of storage.
// The claim is scoped that way on purpose and is not a claim about the whole
// codebase: internal/notification/infrastructure/postgres's
// PreferenceRepository.Set calls Reveal on a secret its own caller handed it
// in the same call, in order to write it to the column, and that write path
// is required and stays. What is asserted — by TestOnlyTheSignerRevealsAStoredSecret
// here, and by cmd/api's TestTheHTTPCompositionRootDoesNotLoadTheSecret —
// is that nothing else reads one back.
func sign(secret domain.Secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret.Reveal()))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// renderTimestamp renders the instant a request is signed at, in Unix
// seconds.
//
// One rendering, used for both the header and the signed value. Two
// renderings of the same instant — even two calls to the same clock — would
// make every receiver's verification fail while every unit test of the
// signature still passed.
func renderTimestamp(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10)
}

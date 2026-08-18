package agentcreds

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The two providers disagree on how expiry is expressed, and getting it wrong
// is silent rather than loud — a millisecond value read as seconds lands in the
// year 56000 and the credential looks permanently fresh. Both conversions live
// here so there is one place to be right.

// epochMillis converts Claude's expiresAt, which is epoch milliseconds.
func epochMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

// epochSeconds converts a JWT exp claim, which RFC 7519 defines in seconds.
func epochSeconds(value int64) time.Time {
	return time.Unix(value, 0).UTC()
}

// jwtExpiry reads the exp claim out of a JWT without verifying its signature.
//
// Captain is not the audience for these tokens and holds none of the keys that
// signed them; it only needs to know when to republish. Decoding the payload
// segment is therefore the whole job, and treating this as authentication would
// be wrong — the value is a scheduling hint about a token whose real validation
// happens at the provider.
func jwtExpiry(token string) (time.Time, error) {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return time.Time{}, fmt.Errorf("not a JWT: expected 3 dot-separated segments, got %d", len(segments))
	}
	// JWTs use unpadded base64url.
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("parse JWT claims: %w", err)
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("JWT carries no exp claim")
	}
	return epochSeconds(claims.Exp), nil
}

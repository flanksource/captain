package captaintoken

import (
	"strings"
	"testing"
)

func TestMintProducesAVerifiableCredential(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatal(err)
	}

	raw := minted.Secret.Value()
	if !strings.HasPrefix(raw, Prefix+"_") {
		t.Fatalf("token %q lacks the %s_ prefix that makes a leak recognizable", raw, Prefix)
	}

	presented, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if presented.ID != minted.ID {
		t.Fatalf("parsed id %q != minted id %q", presented.ID, minted.ID)
	}
	if !presented.Verify(minted.Hash) {
		t.Fatal("a freshly minted token did not verify against its own hash")
	}
}

// The stored hash is what an attacker gets from a database dump. It must not
// contain the credential, and it must not be recomputable without the salt.
func TestHashNeverContainsTheSecret(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	presented, err := Parse(minted.Secret.Value())
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(minted.Hash, presented.secret) {
		t.Fatal("the stored hash embeds the secret")
	}
	if strings.Contains(minted.Hash, minted.Secret.Value()) {
		t.Fatal("the stored hash embeds the whole credential")
	}

	// Salted: the same secret hashed twice must differ, or a dump would reveal
	// which accounts share a credential.
	again, err := HashSecret(presented.secret)
	if err != nil {
		t.Fatal(err)
	}
	if again == minted.Hash {
		t.Fatal("hashing is unsalted; identical secrets produce identical hashes")
	}
	if !VerifySecret(presented.secret, again) {
		t.Fatal("a re-hash of the same secret does not verify")
	}
}

func TestVerifyRejectsAWrongSecret(t *testing.T) {
	first, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	other, err := Parse(second.Secret.Value())
	if err != nil {
		t.Fatal(err)
	}
	if other.Verify(first.Hash) {
		t.Fatal("one token's secret verified against another's hash")
	}
}

// A corrupt row must read as "wrong", never as "skip the check".
func TestVerifyRejectsAMalformedHash(t *testing.T) {
	for _, encoded := range []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=19456,t=1,p=1$only-four-parts",
		"$argon2id$v=19$m=x,t=y,p=z$c2FsdA$a2V5",
		"$bcrypt$v=19$m=19456,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=0,t=0,p=0$c2FsdA$a2V5",
	} {
		if VerifySecret("anything", encoded) {
			t.Fatalf("malformed hash %q verified", encoded)
		}
	}
}

// Every malformed shape gets one answer: distinguishing them tells a prober
// which half they got right.
func TestParseRefusesMalformedCredentialsUniformly(t *testing.T) {
	for _, raw := range []string{
		"",
		"cptn_",
		"cptn_abc",          // no separator
		"cptn_.secret",      // empty id
		"cptn_abc.",         // empty secret
		"bearer cptn_a.b",   // scheme not stripped
		"ghp_somethingelse", // another product's token
		"abc.def",           // no prefix
	} {
		if _, err := Parse(raw); err != ErrMalformed {
			t.Fatalf("Parse(%q) = %v, want ErrMalformed", raw, err)
		}
	}
}

func TestParseToleratesSurroundingWhitespace(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	presented, err := Parse("  " + minted.Secret.Value() + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !presented.Verify(minted.Hash) {
		t.Fatal("a trimmed credential did not verify")
	}
}

// The cache key stands in for the secret in memory, so it must not be the
// secret, and it must separate tokens that differ in either half.
func TestCacheKeyIsDerivedNotLiteral(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	presented, err := Parse(minted.Secret.Value())
	if err != nil {
		t.Fatal(err)
	}

	key := presented.CacheKey()
	if strings.Contains(key, presented.secret) {
		t.Fatal("the cache key contains the secret")
	}
	if !strings.HasPrefix(key, presented.ID) {
		t.Fatalf("cache key %q should be scoped by token id", key)
	}

	wrongSecret := Presented{ID: presented.ID, secret: "different"}
	if wrongSecret.CacheKey() == key {
		t.Fatal("two different secrets share a cache key")
	}
	wrongID := Presented{ID: "other", secret: presented.secret}
	if wrongID.CacheKey() == key {
		t.Fatal("two different token ids share a cache key")
	}
}

func TestBearerFromHeader(t *testing.T) {
	tests := []struct {
		header string
		want   string
		ok     bool
	}{
		{"Bearer cptn_a.b", "cptn_a.b", true},
		{"bearer cptn_a.b", "cptn_a.b", true}, // RFC 7235 scheme is case-insensitive
		{"BEARER  cptn_a.b  ", "cptn_a.b", true},
		{"", "", false},
		{"Bearer", "", false},
		{"Bearer   ", "", false},
		{"Basic dXNlcjpwYXNz", "", false},
		{"cptn_a.b", "", false}, // no scheme
	}
	for _, tt := range tests {
		got, ok := BearerFromHeader(tt.header)
		if got != tt.want || ok != tt.ok {
			t.Errorf("BearerFromHeader(%q) = (%q, %v), want (%q, %v)", tt.header, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseScope(t *testing.T) {
	for _, raw := range []string{"git", "  API  ", "Git"} {
		scope, err := ParseScope(raw)
		if err != nil {
			t.Fatalf("ParseScope(%q): %v", raw, err)
		}
		if !scope.Valid() {
			t.Fatalf("ParseScope(%q) produced an invalid scope %q", raw, scope)
		}
	}
	if _, err := ParseScope(""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty scope err = %v", err)
	}
	if _, err := ParseScope("admin"); err == nil || !strings.Contains(err.Error(), "git, api") {
		t.Fatalf("unknown scope must name the valid set, got %v", err)
	}
}

// Two mints must not collide; the id is a primary lookup key.
func TestMintIsUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for range 50 {
		minted, err := Mint()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[minted.ID]; dup {
			t.Fatalf("duplicate token id %q", minted.ID)
		}
		seen[minted.ID] = struct{}{}
	}
}

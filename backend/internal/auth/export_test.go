package auth

import "github.com/golang-jwt/jwt/v5"

// splitAPIKey is unexported because nothing outside this package should be
// parsing a credential. It is exported to the test because it is a pure
// function and the thing actually worth asserting: the secret half is base64url,
// whose alphabet contains the separator, so getting this wrong corrupts roughly
// one key in three.
var SplitAPIKey = splitAPIKey

// SignForTest mints a correctly signed token with arbitrary claims.
//
// The claim-checking table needs tokens that are wrong in one specific way --
// no expiry, another audience, a nil organization -- and every one of them has
// to carry a valid signature, or each case would pass for the wrong reason.
// Issue cannot produce them, because Issue produces correct tokens.
func SignForTest(t *Tokens, claims map[string]any) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(claims)).SignedString(t.key)
}

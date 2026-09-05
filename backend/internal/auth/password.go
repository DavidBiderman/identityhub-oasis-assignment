package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// ErrBadCredentials is returned when an email is unknown or a password is
// wrong. It is deliberately one error for both.
//
// Telling a caller which of the two failed turns the login form into a way to
// ask whether an address has an account here, and that answer is worth having
// for anyone assembling a list of who to phish.
var ErrBadCredentials = errors.New("The email address or password is incorrect.")

// Password rules. Long enough to be worth hashing, short enough to stay inside
// bcrypt's 72-byte input, which silently truncates beyond it.
const (
	minPasswordLength = 10
	maxPasswordLength = 72
)

// HashPassword returns the value stored in place of a password.
//
// bcrypt, not SHA-256. A password is low-entropy and human-chosen, so what
// protects it is the cost of guessing rather than the size of the space: bcrypt
// makes each guess deliberately expensive, and its work factor can be raised as
// hardware gets faster without changing any stored hash. It also generates and
// stores its own salt, so there is no second column and no chance of a salt
// being reused or forgotten.
//
// This is the opposite reasoning from the API key in apikeys.go, which is
// hashed with plain SHA-256 -- and deliberately so, because that value is 256
// bits from a CSPRNG. There is no dictionary to attack, so a work factor there
// would cost latency on every request and buy nothing. Same table, same word
// "hash", two different problems.
func HashPassword(password string) ([]byte, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash the password: %w", err)
	}
	return hash, nil
}

// VerifyPassword reports whether password matches a stored hash.
//
// bcrypt's comparison is constant time with respect to the hash, so a wrong
// password and a wrong-length password take the same path.
func VerifyPassword(hash []byte, password string) error {
	if err := bcrypt.CompareHashAndPassword(hash, []byte(password)); err != nil {
		return ErrBadCredentials
	}
	return nil
}

// CheckPasswordPolicy rejects a password this application will not store.
//
// A length floor and nothing else. Composition rules -- one capital, one digit,
// one symbol -- push people towards Password1! and are no longer recommended by
// NIST; length is what actually buys entropy. The ceiling is bcrypt's, and it
// is enforced rather than ignored because bcrypt truncates silently past it,
// which would make the tail of a long passphrase decorative.
func CheckPasswordPolicy(password string) error {
	switch {
	case utf8.RuneCountInString(strings.TrimSpace(password)) < minPasswordLength:
		return fmt.Errorf("A password must be at least %d characters.", minPasswordLength)
	case len(password) > maxPasswordLength:
		return fmt.Errorf("A password must be at most %d bytes.", maxPasswordLength)
	}
	return nil
}

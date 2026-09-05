// Package crypto encrypts and decrypts connector credentials. It does nothing
// else, and it is two functions.
//
// There is no type here. A keeper is the key service client, gocloud.dev/secrets
// owns it, and a struct whose only field is that keeper would add a name and no
// behaviour. Encrypt and Decrypt take the keeper as an argument, which is also
// what makes it obvious that they hold no state between calls.
//
// The key service is reached through the Go CDK (gocloud.dev/secrets), so the
// provider is a URL: awskms://, gcpkms://, azurekeyvault://, hashivault://, or
// base64key:// for a local key. Switching provider is configuration.
//
// # Why there is no envelope
//
// An earlier version generated a per-credential data key, wrapped it with the
// key service, and stored the wrapped key, a nonce and the ciphertext in three
// columns. That is the right shape for large payloads: it avoids sending
// megabytes through a key service that has a small request limit.
//
// A connector configuration is roughly 300 bytes, and AWS KMS accepts 4 KB.
// So the payload goes to the key service directly, and one opaque blob comes
// back. That removes a data key, a nonce, two database columns and the code
// that managed them, with no loss: the same key service protects the same
// secret, and the number of network round trips per operation is unchanged.
//
// The nonce did not go away because it stopped being necessary -- AES-GCM
// requires one, and reusing a nonce under one key is catastrophic. It went away
// because the provider now generates and carries it inside its own ciphertext,
// which is a better place for it than a column we have to keep correct.
package crypto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gocloud.dev/secrets"

	// Providers registered for secrets.OpenKeeper. AWS and a local key cover
	// what this project runs against; the others are one import line each.
	_ "gocloud.dev/secrets/awskms"
	_ "gocloud.dev/secrets/localsecrets"
)

// maxPlaintext bounds what is sent to the key service.
//
// AWS KMS rejects a plaintext over 4096 bytes, and the other providers have
// comparable limits. Failing here with a clear message beats a provider error
// that a caller has to decode.
const maxPlaintext = 4000

// maxCiphertext bounds what is read back from the database before it is sent
// for decryption. The value is ours, but a bound stops a corrupted row from
// becoming a large request.
const maxCiphertext = 16 << 10

// ErrDecrypt is returned whenever a payload cannot be recovered. It is
// deliberately opaque: a caller must not be able to distinguish a corrupted
// ciphertext from a scope mismatch from a wrong key.
var ErrDecrypt = errors.New("the credential could not be decrypted")

// OpenKeeper connects to the key service named by keeperURL.
//
// It exists rather than calling secrets.OpenKeeper directly for one reason: the
// URL may contain key material -- base64key:// embeds the key itself -- and the
// Go CDK quotes the URL it was given when it fails. This refuses a URL that
// names no provider, and redacts the URL out of the error when one does fail.
func OpenKeeper(ctx context.Context, keeperURL string) (*secrets.Keeper, error) {
	if strings.TrimSpace(keeperURL) == "" {
		return nil, fmt.Errorf("a keeper URL is required")
	}
	parsed, err := url.Parse(keeperURL)
	if err != nil || parsed.Scheme == "" {
		return nil, fmt.Errorf("the keeper URL %q is not a URL such as awskms://alias/identityhub", keeperURL)
	}

	keeper, err := secrets.OpenKeeper(ctx, keeperURL)
	if err != nil {
		return nil, fmt.Errorf("open the %q key service: %w", parsed.Scheme, redactURL(err, keeperURL))
	}
	return keeper, nil
}

// Encrypt encrypts plaintext for the given scope.
//
// The scope -- an organization and account -- goes inside the ciphertext and is
// checked on the way out, so a row copied between accounts fails to decrypt
// rather than silently succeeding. gocloud.dev/secrets exposes no
// additional-authenticated-data parameter, so binding it into the plaintext is
// what makes that check possible.
func Encrypt(ctx context.Context, keeper *secrets.Keeper, scope string, plaintext []byte) ([]byte, error) {
	switch {
	case scope == "":
		return nil, fmt.Errorf("encrypting requires a scope to bind the ciphertext to")
	case strings.ContainsRune(scope, 0):
		// The scope is NUL-framed below; one inside it would make that framing
		// ambiguous.
		return nil, fmt.Errorf("a scope must not contain a NUL byte")
	case len(plaintext) == 0:
		return nil, fmt.Errorf("there is nothing to encrypt")
	case len(scope)+1+len(plaintext) > maxPlaintext:
		return nil, fmt.Errorf("the payload is %d bytes, over the %d byte limit",
			len(scope)+1+len(plaintext), maxPlaintext)
	}

	blob, err := keeper.Encrypt(ctx, bindScope(scope, plaintext))
	if err != nil {
		return nil, fmt.Errorf("encrypt with the key service: %w", err)
	}
	return blob, nil
}

// Decrypt recovers a ciphertext encrypted for the given scope. Every failure
// returns ErrDecrypt so that the error cannot be used to probe why.
func Decrypt(ctx context.Context, keeper *secrets.Keeper, scope string, blob []byte) ([]byte, error) {
	if scope == "" {
		return nil, fmt.Errorf("decrypting requires the scope the ciphertext was bound to")
	}
	if len(blob) == 0 || len(blob) > maxCiphertext {
		return nil, ErrDecrypt
	}

	bound, err := keeper.Decrypt(ctx, blob)
	if err != nil {
		return nil, ErrDecrypt
	}
	defer zero(bound)

	plaintext, err := unbindScope(scope, bound)
	if err != nil {
		return nil, ErrDecrypt
	}
	// The buffer above is zeroed on return, so the payload is copied out.
	out := make([]byte, len(plaintext))
	copy(out, plaintext)
	return out, nil
}

// bindScope frames a scope and a payload as "scope\x00payload".
func bindScope(scope string, plaintext []byte) []byte {
	out := make([]byte, 0, len(scope)+1+len(plaintext))
	out = append(out, scope...)
	out = append(out, 0)
	return append(out, plaintext...)
}

// unbindScope reverses bindScope, rejecting a payload encrypted for another scope.
func unbindScope(scope string, bound []byte) ([]byte, error) {
	prefix, payload, found := bytes.Cut(bound, []byte{0})
	if !found || !bytes.Equal(prefix, []byte(scope)) {
		return nil, ErrDecrypt
	}
	return payload, nil
}

// redactURL removes a keeper URL from an error message.
//
// A base64key:// URL carries the key itself, and the Go CDK quotes the URL it
// was given when it fails. The message is rebuilt rather than wrapped, because
// wrapping would keep the original reachable through Unwrap.
func redactURL(err error, keeperURL string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), keeperURL, "[redacted keeper URL]"))
}

// zero overwrites a decrypted buffer once it is no longer needed.
func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

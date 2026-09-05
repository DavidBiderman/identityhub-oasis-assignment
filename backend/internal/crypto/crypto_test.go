package crypto_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"gocloud.dev/secrets"
	"gocloud.dev/secrets/localsecrets"

	"github.com/dbiderman/identityhub/backend/internal/crypto"
)

// newKeeper returns a key service over a local random key. The provider is
// irrelevant to the properties under test: scope binding happens above the
// keeper, identically for every provider.
func newKeeper(t *testing.T) *secrets.Keeper {
	t.Helper()
	key, err := localsecrets.NewRandomKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keeper, err := crypto.OpenKeeper(context.Background(),
		"base64key://"+base64.URLEncoding.EncodeToString(key[:]))
	if err != nil {
		t.Fatalf("open keeper: %v", err)
	}
	t.Cleanup(func() { _ = keeper.Close() })
	return keeper
}

const scopeA = "org-1/acct-a"
const scopeB = "org-1/acct-b"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	secret := []byte("jira-api-token-ATATT3xFfGF0")

	env, err := crypto.Encrypt(context.Background(), keeper, scopeA, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := crypto.Decrypt(context.Background(), keeper, scopeA, env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round trip mismatch: got %q want %q", got, secret)
	}
}

func TestEncryptDoesNotLeakPlaintext(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	secret := []byte("jira-api-token-ATATT3xFfGF0")

	env, err := crypto.Encrypt(context.Background(), keeper, scopeA, secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(env, secret) {
		t.Error("the stored ciphertext contains the plaintext credential")
	}
	// The scope is inside the ciphertext, not beside it.
	if bytes.Contains(env, []byte(scopeA)) {
		t.Error("the stored ciphertext exposes the scope in the clear")
	}
}

// The central multi-tenancy guarantee: a credential encrypted for one account is
// undecryptable under another, including a sibling account in the same org.
func TestDecryptRejectsAForeignScope(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	env, err := crypto.Encrypt(context.Background(), keeper, scopeA, []byte("tenant a credential"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := crypto.Decrypt(context.Background(), keeper, scopeB, env); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("cross-account open: got %v, want ErrDecrypt", err)
	}
}

// A ciphertext from one account must not decrypt under another, which is what
// binding the scope inside the payload buys. Without it, moving a row between
// accounts would succeed silently.
func TestCiphertextCannotBeMovedBetweenAccounts(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	ctx := context.Background()

	a, err := crypto.Encrypt(ctx, keeper, scopeA, []byte("account a credential"))
	if err != nil {
		t.Fatalf("encrypt for account a: %v", err)
	}
	b, err := crypto.Encrypt(ctx, keeper, scopeB, []byte("account b credential"))
	if err != nil {
		t.Fatalf("encrypt for account b: %v", err)
	}

	// Both directions: neither row is usable in the other's account.
	if _, err := crypto.Decrypt(ctx, keeper, scopeB, a); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("a under scope b: got %v, want ErrDecrypt", err)
	}
	if _, err := crypto.Decrypt(ctx, keeper, scopeA, b); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("b under scope a: got %v, want ErrDecrypt", err)
	}
}

func TestDecryptRejectsATamperedCiphertext(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	env, err := crypto.Encrypt(context.Background(), keeper, scopeA, []byte("tenant a credential"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	env[0] ^= 0xFF
	if _, err := crypto.Decrypt(context.Background(), keeper, scopeA, env); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("tampered ciphertext: got %v, want ErrDecrypt", err)
	}
}

func TestEncryptingTheSameSecretTwiceProducesDifferentCiphertexts(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	ctx := context.Background()
	secret := []byte("same credential twice")

	first, err := crypto.Encrypt(ctx, keeper, scopeA, secret)
	if err != nil {
		t.Fatalf("encrypt first: %v", err)
	}
	second, err := crypto.Encrypt(ctx, keeper, scopeA, secret)
	if err != nil {
		t.Fatalf("encrypt second: %v", err)
	}
	// Identical plaintexts must not produce identical ciphertexts, or a reader
	// of the table could tell which accounts share a credential.
	if bytes.Equal(first, second) {
		t.Error("identical plaintexts produced identical ciphertexts")
	}
}

// Malformed rows must be rejected without panicking or allocating on a
// length taken from the data.
func TestDecryptRejectsMalformedCiphertext(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	good, err := crypto.Encrypt(context.Background(), keeper, scopeA, []byte("x"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	cases := map[string][]byte{
		"empty":     {},
		"garbage":   []byte("nonsense"),
		"truncated": good[:len(good)/2],
		"oversized": make([]byte, 17<<10),
	}
	for name, env := range cases {
		if _, err := crypto.Decrypt(context.Background(), keeper, scopeA, env); !errors.Is(err, crypto.ErrDecrypt) {
			t.Errorf("%s: got %v, want ErrDecrypt", name, err)
		}
	}
}

func TestEncryptRequiresAScope(t *testing.T) {
	t.Parallel()

	keeper := newKeeper(t)
	if _, err := crypto.Encrypt(context.Background(), keeper, "", []byte("x")); err == nil {
		t.Fatal("encrypt with an empty scope: got nil error, want failure")
	}
	if _, err := crypto.Encrypt(context.Background(), keeper, "org\x001/acct", []byte("x")); err == nil {
		t.Fatal("encrypt with a NUL in the scope: got nil error, want failure")
	}
}

// A base64key:// URL embeds the key itself, so it must not survive into an
// error message.
func TestOpenKeeperRedactsTheKeeperURL(t *testing.T) {
	t.Parallel()

	key, err := localsecrets.NewRandomKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	secretURL := "base64key://" + base64.URLEncoding.EncodeToString(key[:]) + "&bad=query"

	if _, err := crypto.OpenKeeper(context.Background(), secretURL); err != nil {
		if bytes.Contains([]byte(err.Error()), key[:8]) {
			t.Errorf("error leaked key material: %v", err)
		}
	}
	if _, err := crypto.OpenKeeper(context.Background(), ""); err == nil {
		t.Error("empty keeper URL was accepted")
	}
	if _, err := crypto.OpenKeeper(context.Background(), "not-a-url"); err == nil {
		t.Error("scheme-less keeper URL was accepted")
	}
}

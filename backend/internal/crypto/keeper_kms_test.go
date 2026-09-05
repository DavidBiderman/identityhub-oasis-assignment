package crypto_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/dbiderman/identityhub/backend/internal/crypto"
)

// TestEncryptDecryptAgainstKMS runs the same properties against a real KMS wire
// protocol rather than a local key, which is what makes the Go CDK abstraction
// worth having. Locally that service is floci; in CI or production it would be
// AWS. Skipped when no endpoint is configured, so `go test ./...` needs no
// containers.
func TestEncryptDecryptAgainstKMS(t *testing.T) {
	if os.Getenv("AWS_ENDPOINT_URL") == "" {
		t.Skip("AWS_ENDPOINT_URL not set; skipping KMS integration test")
	}
	ctx := context.Background()

	// The AWS SDK reads AWS_ENDPOINT_URL, AWS_REGION and the static test
	// credentials from the environment, so the keeper URL below needs no
	// provider-specific wiring.
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	created, err := kms.NewFromConfig(cfg).CreateKey(ctx, &kms.CreateKeyInput{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	keyID := *created.KeyMetadata.KeyId

	keeper, err := crypto.OpenKeeper(ctx, "awskms://"+keyID)
	if err != nil {
		t.Fatalf("open keeper: %v", err)
	}
	defer func() { _ = keeper.Close() }()

	secret := []byte("jira-api-token-ATATT3xFfGF0")
	env, err := crypto.Encrypt(ctx, keeper, "org-1/acct-a", secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(env, secret) {
		t.Error("the stored ciphertext leaked the plaintext credential")
	}

	got, err := crypto.Decrypt(ctx, keeper, "org-1/acct-a", env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round trip mismatch: got %q want %q", got, secret)
	}
	if _, err := crypto.Decrypt(ctx, keeper, "org-1/acct-b", env); !errors.Is(err, crypto.ErrDecrypt) {
		t.Fatalf("cross-account open: got %v, want ErrDecrypt", err)
	}
}

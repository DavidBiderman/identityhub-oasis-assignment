package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// maxConfigBytes bounds a decrypted configuration before it is parsed. The
// value comes from our own database, but a bound keeps a corrupted row from
// becoming a large allocation.
const maxConfigBytes = 64 << 10

// StrictDecode is a helper connectors use inside their own DecodeConfig.
//
// Decoding is each connector's responsibility -- only it knows what fields it
// needs -- but the checks that make a decode safe are the same everywhere, so
// they live here rather than being re-implemented and subtly varied:
//
//  1. reject an empty or oversized blob,
//  2. reject unknown fields, since a blob that does not match the struct is
//     more likely to be another connector's data than a harmless extra,
//  3. reject trailing data,
//  4. run the configuration's own Validate.
//
// Parse errors are not wrapped: encoding/json quotes the offending input, and
// that input is a credential.
func StrictDecode(plaintext []byte, dst Config) error {
	if dst == nil {
		return Errorf(ErrConfigInvalid, "No destination type was supplied to decode into.")
	}
	if len(plaintext) == 0 {
		return Errorf(ErrConfigInvalid, "The configuration is empty.")
	}
	if len(plaintext) > maxConfigBytes {
		return Errorf(ErrConfigInvalid, "The configuration is larger than %d bytes.", maxConfigBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(plaintext))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return Errorf(ErrConfigInvalid, "The configuration is not valid JSON for a %s connection.", dst.ConnectorType())
	}
	if dec.More() {
		return Errorf(ErrConfigInvalid, "The configuration contains more than one JSON value.")
	}
	// A connector that named the offending field keeps that structure; one
	// that returned a plain error is wrapped so it is still classified.
	if err := dst.Validate(); err != nil {
		var classified *Error
		if errors.As(err, &classified) {
			return classified
		}
		return Errorf(ErrConfigInvalid, "%s", err)
	}
	return nil
}

// EncodeConfig serialises a configuration for encryption, validating it first.
//
// The connect flow stores the result of this rather than the bytes a caller
// supplied, because a connector completes its own configuration -- normalising
// what was typed, and filling in what can only be discovered by talking to the
// provider. Storing the raw input would throw that away.
func EncodeConfig(cfg Config) ([]byte, error) {
	if cfg == nil {
		return nil, Errorf(ErrConfigInvalid, "There is no configuration to store.")
	}
	if err := cfg.Validate(); err != nil {
		var classified *Error
		if errors.As(err, &classified) {
			return nil, classified
		}
		return nil, Errorf(ErrConfigInvalid, "%s", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode %s configuration: %w", cfg.ConnectorType(), err)
	}
	return encoded, nil
}

package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dbiderman/identityhub/backend/internal/config"
)

// config is everything the bootstrap step reads.
//
// It creates the key that credentials are encrypted under, so it names that key
// directly rather than through a keeper URL: creating a key and using one are
// different operations, and only this binary does the former.
type bootstrapConfig struct {
	config.Core

	KMSAlias string `env:"KMS_ALIAS,default=alias/identityhub"`
}

// Validate reports whether the configuration is usable.
func (c bootstrapConfig) Validate() error {
	errs := []error{c.Core.Validate()}
	if strings.TrimSpace(c.KMSAlias) == "" {
		errs = append(errs, fmt.Errorf("KMS_ALIAS is required"))
	}
	return errors.Join(errs...)
}

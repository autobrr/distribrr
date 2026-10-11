// Package token generates and checks the shared API token.
package token

import (
	"crypto/rand"

	"github.com/pkg/errors"
)

// MinLength is the shortest token distribrr accepts.
const MinLength = 16

// sample is the placeholder token older example configs shipped with.
const sample = "MY_SECRET_TOKEN"

const hint = "generate one with: distribrr token generate"

// Generate returns a random token with 128 bits of entropy.
func Generate() string {
	return rand.Text()
}

// Check returns why t cannot be used as an API token, or nil if it can.
func Check(t string) error {
	switch {
	case t == "":
		return errors.Errorf("must be set; %s", hint)
	case t == sample:
		return errors.Errorf("is the example token %s; %s", sample, hint)
	case len(t) < MinLength:
		return errors.Errorf("is %d characters, want at least %d; %s", len(t), MinLength, hint)
	}

	return nil
}

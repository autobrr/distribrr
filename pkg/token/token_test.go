package token

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerate(t *testing.T) {
	a, b := Generate(), Generate()

	assert.NoError(t, Check(a))
	assert.NotEqual(t, a, b)
}

func TestCheck(t *testing.T) {
	tests := []struct {
		token string
		want  string
	}{
		{token: "", want: "must be set"},
		{token: "MY_SECRET_TOKEN", want: "example token"},
		{token: "short", want: "is 5 characters, want at least 16"},
		{token: "0123456789abcdef"},
	}

	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			err := Check(tt.token)
			if tt.want == "" {
				assert.NoError(t, err)
				return
			}

			assert.ErrorContains(t, err, tt.want)
			assert.ErrorContains(t, err, "distribrr token generate")
		})
	}
}

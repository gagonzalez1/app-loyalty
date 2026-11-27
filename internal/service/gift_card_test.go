package service

import (
	"errors"
	"slices"
	"testing"
)

func TestGiftCardSocialNetworkOrderAndValidation(t *testing.T) {
	canonical := []string{"instagram", "tiktok", "facebook", "linkedin"}
	for _, test := range []struct {
		name     string
		input    []string
		expected []string
		invalid  bool
	}{
		{name: "omitted"},
		{name: "all in canonical order", input: []string{"linkedin", "facebook", "tiktok", "instagram"}, expected: canonical},
		{name: "subset", input: []string{"facebook", "tiktok"}, expected: []string{"tiktok", "facebook"}},
		{name: "duplicate", input: []string{"instagram", "instagram"}, invalid: true},
		{name: "unsupported", input: []string{"sitio_web"}, invalid: true},
		{name: "external URL", input: []string{"https://example.test"}, invalid: true},
		{name: "too many", input: append(append([]string{}, canonical...), "instagram"), invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := orderGiftCardSocialNetworks(test.input)
			if test.invalid {
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("expected invalid request, got %v", err)
				}
				return
			}
			if err != nil || !slices.Equal(got, test.expected) {
				t.Fatalf("ordered networks = %v, err = %v", got, err)
			}
		})
	}
}

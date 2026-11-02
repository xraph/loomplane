package provider_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/xraph/loomplane/pkg/provider"
)

func TestMatchDomainPriceComparesDecimals(t *testing.T) {
	approved := provider.DomainPrice{Currency: "USD", RegistrationCost: "10.11"}
	if err := provider.MatchDomainPrice(approved, provider.DomainPrice{Currency: "USD", RegistrationCost: "10.110"}); err != nil {
		t.Fatal(err)
	}
	for _, current := range []provider.DomainPrice{
		{Currency: "EUR", RegistrationCost: "10.11"},
		{Currency: "USD", RegistrationCost: "12.00"},
		{Currency: "USD", RegistrationCost: ""},
		{Currency: "USD", RegistrationCost: "ten"},
	} {
		if err := provider.MatchDomainPrice(approved, current); !errors.Is(err, provider.ErrInfrastructureDrift) {
			t.Fatalf("%+v: %v", current, err)
		}
	}
	if err := provider.MatchDomainPrice(provider.DomainPrice{Currency: "USD"}, provider.DomainPrice{Currency: "USD"}); !errors.Is(err, provider.ErrInfrastructureDrift) {
		t.Fatalf("empty approved cost: %v", err)
	}
}

func TestRegistrantContactNeverFormats(t *testing.T) {
	c := provider.RegistrantContact{Email: "rex@example.com"}
	if fmt.Sprintf("%v %+v %#v", c, c, c) != "[registrant contact] [registrant contact] [registrant contact]" {
		t.Fatal("contact leaked")
	}
}

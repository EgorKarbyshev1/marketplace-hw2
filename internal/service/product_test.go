package service

import (
	"encoding/json"
	"testing"

	"marketplace/internal/generated"
)

func TestValidatePrice(t *testing.T) {
	tests := []struct {
		price string
		valid bool
	}{
		{"0.01", true}, {"1999.99", true}, {"9999999999.99", true},
		{"1.2300", true}, {"1e2", true}, {"0", false}, {"-1", false},
		{"0.001", false}, {"10000000000", false}, {"9999999999.991", false},
		{"NaN", false}, {"", false},
	}
	for _, test := range tests {
		t.Run(test.price, func(t *testing.T) {
			err := ValidatePrice(json.Number(test.price))
			if (err == nil) != test.valid {
				t.Fatalf("ValidatePrice(%q) = %v, valid = %v", test.price, err, test.valid)
			}
		})
	}
}

func TestValidateProduct(t *testing.T) {
	input := generated.ProductCreate{
		Name: "Клавиатура", Category: "electronics", Price: json.Number("1999.99"),
		Stock: 0, Status: generated.ACTIVE,
	}
	if err := validateProduct(input); err != nil {
		t.Fatal(err)
	}
	input.Status = generated.ProductStatus("UNKNOWN")
	if err := validateProduct(input); err == nil {
		t.Fatal("unknown status accepted")
	}
}

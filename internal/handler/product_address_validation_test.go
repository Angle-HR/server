package handler

import (
	"encoding/json"
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestProductAddressSearchPayload(t *testing.T) {
	var req productAddressBody
	if err := json.Unmarshal([]byte(`{"country_id":"a1b2c3d4-e5f6-4789-a012-3456789abcde","entry_mode":"search","city":"Addlestone","state_or_county":"England","formatted_address":"Addlestone, UK","identification":{"registration_number":"66767676"}}`), &req); err != nil {
		t.Fatal(err)
	}
	v := validator.New()
	if err := v.Struct(req); err != nil {
		t.Fatalf("search address should allow omitted street and postcode: %v", err)
	}
	req.EntryMode = "manual"
	err := v.Struct(req)
	fields, ok := err.(validator.ValidationErrors)
	if !ok || len(fields) != 2 {
		t.Fatalf("manual address should require street and postcode: %v", err)
	}
	for _, field := range fields {
		if (field.Field() != "Line1" && field.Field() != "PostCode") || field.Tag() != "required_if" {
			t.Fatalf("unexpected validation error: %v", field)
		}
	}
	req.Line1 = "1 High Street"
	req.PostCode = "KT15 1AA"
	if err := v.Struct(req); err != nil {
		t.Fatalf("complete manual address should pass: %v", err)
	}
}

package orghttp

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/pashagolub/pgxmock/v4"

	"github.com/Angle-HR/server/pkg/response"
)

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status: got %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()

	var envelope response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	raw, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}

	var data map[string]string
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}

	return data
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) response.ErrorBody {
	t.Helper()

	var envelope response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	if envelope.Error == nil {
		t.Fatal("expected error body")
	}

	return *envelope.Error
}

func assertMocksMet(t *testing.T, mocks ...pgxmock.PgxPoolIface) {
	t.Helper()

	for _, mock := range mocks {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	}
}

func decodeDetailsFields(t *testing.T, body response.ErrorBody) []map[string]string {
	t.Helper()

	raw, ok := body.Details["fields"]
	if !ok {
		t.Fatalf("details: missing \"fields\" key; got %v", body.Details)
	}

	rawSlice, ok := raw.([]any)
	if !ok {
		t.Fatalf("details.fields: expected []any, got %T", raw)
	}

	result := make([]map[string]string, 0, len(rawSlice))
	for _, item := range rawSlice {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("details.fields item: expected map[string]any, got %T", item)
		}
		entry := make(map[string]string, len(m))
		for k, v := range m {
			str, isString := v.(string)
			if !isString {
				t.Fatalf("details.fields value: expected string, got %T", v)
			}
			entry[k] = str
		}
		result = append(result, entry)
	}

	return result
}

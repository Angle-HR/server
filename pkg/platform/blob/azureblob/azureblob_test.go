package azureblob_test

import (
	"context"
	"os"
	"testing"

	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/platform/blob/azureblob"
	"github.com/Angle-HR/server/pkg/platform/blob/blobtest"
)

// azuriteKey is the published, well-known development key of the Azurite emulator, not a secret.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

// TestConformance runs against Azurite given by BLOB_AZURE_ENDPOINT, for example
// http://127.0.0.1:20910/devstoreaccount1. It is skipped when unset.
func TestConformance(t *testing.T) {
	endpoint := os.Getenv("BLOB_AZURE_ENDPOINT")
	if endpoint == "" {
		t.Skip("BLOB_AZURE_ENDPOINT not set")
	}
	cfg := azureblob.Config{
		AccountName: "devstoreaccount1", AccountKey: azuriteKey, Container: "blobtest", Endpoint: endpoint,
	}
	blobtest.Run(t, func(t *testing.T) blob.BlobStore {
		s, err := azureblob.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.EnsureContainer(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

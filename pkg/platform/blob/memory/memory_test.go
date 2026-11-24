package memory_test

import (
	"testing"

	"github.com/Angle-HR/server/pkg/platform/blob"
	"github.com/Angle-HR/server/pkg/platform/blob/blobtest"
	"github.com/Angle-HR/server/pkg/platform/blob/memory"
)

func TestConformance(t *testing.T) {
	blobtest.Run(t, func(*testing.T) blob.BlobStore { return memory.New() })
}

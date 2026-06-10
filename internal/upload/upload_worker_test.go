package upload

import (
	"context"
	"testing"

	fluvio "github.com/software78/fluvio"
)

func TestUploadWorker_Work_Success(t *testing.T) {
	t.Parallel()

	w := &UploadWorker{}
	job := &fluvio.Job[Args]{
		Args: Args{
			Bucket:    "uploads",
			ObjectKey: "tenant/abc/file.pdf",
		},
	}

	if err := w.Work(context.Background(), job); err != nil {
		t.Fatalf("expected Work to succeed, got error: %v", err)
	}
}

package upload

// Args defines job queue arguments for upload processing.
type Args struct {
	ObjectKey string `json:"object_key"`
	Bucket    string `json:"bucket"`
}

// Kind returns the job kind name.
func (Args) Kind() string { return "upload" }

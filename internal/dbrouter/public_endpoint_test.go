package dbrouter

import "testing"

func TestLoadConfigsFromEnv_publicEndpoint(t *testing.T) {
	setFullEnv(t)
	t.Setenv("R2_ENDPOINT", "http://minio:9000")
	t.Setenv("R2_PUBLIC_ENDPOINT", "http://localhost:39000")
	t.Setenv("ANGLEHR_EU_R2_PUBLIC_ENDPOINT", "https://files.example.com")

	configs, err := LoadConfigsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range configs {
		if c.Region == "eu" {
			if c.R2PublicEndpoint != "files.example.com" || !c.R2PublicUseSSL {
				t.Errorf("eu: %q ssl=%v", c.R2PublicEndpoint, c.R2PublicUseSSL)
			}
			continue
		}
		if c.R2Endpoint != "minio:9000" || c.R2PublicEndpoint != "localhost:39000" || c.R2PublicUseSSL {
			t.Errorf("%s: endpoint %q public %q ssl=%v", c.Region, c.R2Endpoint, c.R2PublicEndpoint, c.R2PublicUseSSL)
		}
	}
}

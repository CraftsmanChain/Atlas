package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGPUCatalogFiltersCPUHostsCaseInsensitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assets.csv")
	content := "\ufeffnode_ip,name,tags\n10.114.4.21,4090gpu-01,RTX4090\n10.114.4.22,H100GPU-02,H100\n10.114.4.23,cpu-23,CPU\n10.114.4.24,ubuntu,CPU\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := loadGPUCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 || catalog["10.114.4.21"] != "4090gpu-01" || catalog["10.114.4.22"] != "H100GPU-02" {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	if _, exists := catalog["10.114.4.23"]; exists {
		t.Fatal("CPU hostname must not enter GPU catalog")
	}
}

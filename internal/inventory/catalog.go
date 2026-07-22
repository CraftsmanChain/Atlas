package inventory

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
)

// loadGPUCatalog reads the operator-maintained asset document exported as
// CSV. A node belongs to GPU inventory only when its hostname contains "gpu"
// (case-insensitive). This prevents BMC/node targets from pulling CPU hosts
// into the GPU asset domain.
func loadGPUCatalog(path string) (map[string]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open inventory asset file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read inventory asset header: %w", err)
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))] = index
	}
	ipColumn, ipOK := columns["node_ip"]
	nameColumn, nameOK := columns["name"]
	if !ipOK || !nameOK {
		return nil, fmt.Errorf("inventory asset file requires node_ip and name columns")
	}

	result := make(map[string]string)
	for {
		row, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read inventory asset row: %w", readErr)
		}
		if ipColumn >= len(row) || nameColumn >= len(row) {
			continue
		}
		ip, hostname := strings.TrimSpace(row[ipColumn]), strings.TrimSpace(row[nameColumn])
		if ip != "" && isGPUHostname(hostname) {
			result[ip] = hostname
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("inventory asset file contains zero GPU hostnames")
	}
	return result, nil
}

func isGPUHostname(hostname string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(hostname)), "gpu")
}

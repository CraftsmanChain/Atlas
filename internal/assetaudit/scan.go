package assetaudit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// defaultMask is only used when the sheet does not declare a mask for a row.
const defaultMask = 24

// maxScanTargets bounds one scan so a mistyped mask cannot turn into an
// unbounded sweep of the private address space.
const maxScanTargets = 131072

// ScanFinding is one live address that has no record in the asset registry.
type ScanFinding struct {
	IPAddress string `json:"ip_address"`
	Subnet    string `json:"subnet"`
}

// ScanSubnetSummary reports per-subnet sweep coverage.
type ScanSubnetSummary struct {
	CIDR  string `json:"cidr"`
	Hosts int    `json:"hosts"`
	Live  int    `json:"live"`
}

// ScanSummary is the headline result of one live-IP scan.
type ScanSummary struct {
	Subnets        []ScanSubnetSummary `json:"subnets"`
	SubnetCount    int                 `json:"subnet_count"`
	KnownAssetIPs  int                 `json:"known_asset_ips"`
	ScannedTargets int                 `json:"scanned_targets"`
	LiveCount      int                 `json:"live_count"`
	Unregistered   int                 `json:"unregistered"`
	Truncated      bool                `json:"truncated"`
	GeneratedAt    time.Time           `json:"generated_at"`
}

type scanTarget struct {
	ip     string
	subnet string
}

type subnet struct {
	cidr *net.IPNet
}

// runScan derives the cluster subnets, sweeps the addresses Atlas has no asset
// record for, and returns the live ones.
func (s *Service) runScan(ctx context.Context, fetch configForFetch, now time.Time, timeoutMS int) (ScanSummary, []ScanFinding, error) {
	rows, err := s.fetchDeliveryRows(ctx, fetch)
	if err != nil {
		return ScanSummary{}, nil, err
	}
	parsed, err := parseDeliveryRows(rows)
	if err != nil {
		return ScanSummary{}, nil, err
	}
	known, err := s.knownAssetIPs()
	if err != nil {
		return ScanSummary{}, nil, err
	}
	summary := ScanSummary{KnownAssetIPs: len(known), GeneratedAt: now}

	subnets := collectSubnets(parsed)
	// Addresses Atlas already tracks may belong to a subnet the sheet does not
	// describe; cover them at /24 unless an existing subnet already contains
	// them.
	for ip := range known {
		parsedIP := net.ParseIP(ip)
		if parsedIP == nil || containsIP(subnets, parsedIP) {
			continue
		}
		if _, network, maskErr := net.ParseCIDR(fmt.Sprintf("%s/%d", ip, defaultMask)); maskErr == nil {
			subnets = addSubnet(subnets, network)
		}
	}
	sort.Slice(subnets, func(left, right int) bool { return subnets[left].cidr.String() < subnets[right].cidr.String() })

	targets := make([]scanTarget, 0, 4096)
	counters := make(map[string]*ScanSubnetSummary, len(subnets))
	seen := make(map[string]struct{})
	for _, item := range subnets {
		cidr := item.cidr.String()
		stat := &ScanSubnetSummary{CIDR: cidr}
		counters[cidr] = stat
		forEachHost(item.cidr, func(ip net.IP) bool {
			address := ip.String()
			if _, exists := seen[address]; exists {
				return true
			}
			seen[address] = struct{}{}
			if _, exists := known[address]; exists {
				return true
			}
			if len(targets) >= maxScanTargets {
				summary.Truncated = true
				return false
			}
			targets = append(targets, scanTarget{ip: address, subnet: cidr})
			stat.Hosts++
			return true
		})
		if summary.Truncated {
			break
		}
	}
	summary.SubnetCount = len(subnets)
	summary.ScannedTargets = len(targets)
	if len(targets) == 0 {
		return summary, []ScanFinding{}, nil
	}

	if timeoutMS <= 0 {
		timeoutMS = 1000
	}
	alive, err := s.sweep(ctx, targets, timeoutMS)
	if err != nil {
		return summary, nil, err
	}
	findings := make([]ScanFinding, 0, len(alive))
	for _, target := range targets {
		if !alive[target.ip] {
			continue
		}
		findings = append(findings, ScanFinding{IPAddress: target.ip, Subnet: target.subnet})
		if stat, ok := counters[target.subnet]; ok {
			stat.Live++
		}
	}
	sort.Slice(findings, func(left, right int) bool { return compareIP(findings[left].IPAddress, findings[right].IPAddress) < 0 })
	summary.Subnets = make([]ScanSubnetSummary, 0, len(subnets))
	for _, item := range subnets {
		if stat, ok := counters[item.cidr.String()]; ok {
			summary.Subnets = append(summary.Subnets, *stat)
		}
	}
	summary.LiveCount = len(findings)
	summary.Unregistered = len(findings)
	return summary, findings, nil
}

// collectSubnets derives the cluster's IPv4 subnets from the delivered rows
// using each row's declared mask.
func collectSubnets(rows []deliveryRow) []subnet {
	subnets := make([]subnet, 0)
	for _, row := range rows {
		if !row.Delivered || row.IP == "" {
			continue
		}
		prefix := row.Mask
		if prefix == 0 {
			prefix = defaultMask
		}
		_, network, err := net.ParseCIDR(fmt.Sprintf("%s/%d", row.IP, prefix))
		if err != nil || network.IP.To4() == nil {
			continue
		}
		subnets = addSubnet(subnets, network)
	}
	return subnets
}

// addSubnet keeps the set overlap-free: a subnet already covered by an existing
// entry is dropped, and any existing entry covered by the new one is replaced.
func addSubnet(subnets []subnet, candidate *net.IPNet) []subnet {
	for _, item := range subnets {
		if item.cidr.Contains(candidate.IP) {
			return subnets
		}
	}
	kept := subnets[:0]
	for _, item := range subnets {
		if !candidate.Contains(item.cidr.IP) {
			kept = append(kept, item)
		}
	}
	return append(kept, subnet{cidr: candidate})
}

func containsIP(subnets []subnet, ip net.IP) bool {
	for _, item := range subnets {
		if item.cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// forEachHost walks the usable addresses of a subnet without materializing the
// whole range. Returning false stops the walk.
func forEachHost(network *net.IPNet, visit func(net.IP) bool) {
	base := network.IP.To4()
	mask := network.Mask
	if base == nil || len(mask) != 4 {
		return
	}
	ones, bits := mask.Size()
	if bits != 32 {
		return
	}
	if ones >= 31 {
		visit(append(net.IP(nil), base...))
		return
	}
	current := make(net.IP, 4)
	copy(current, base)
	last := broadcastIPv4(base, mask)
	for {
		incrementIPv4(current)
		if current.Equal(last) {
			return
		}
		if !visit(append(net.IP(nil), current...)) {
			return
		}
	}
}

func broadcastIPv4(base net.IP, mask net.IPMask) net.IP {
	result := make(net.IP, 4)
	for index := 0; index < 4; index++ {
		result[index] = base[index] | ^mask[index]
	}
	return result
}

func incrementIPv4(ip net.IP) {
	for index := len(ip) - 1; index >= 0; index-- {
		ip[index]++
		if ip[index] != 0 {
			return
		}
	}
}

func compareIP(left, right string) int {
	a, b := net.ParseIP(left).To4(), net.ParseIP(right).To4()
	if a == nil || b == nil {
		return strings.Compare(left, right)
	}
	for index := 0; index < 4; index++ {
		if a[index] != b[index] {
			return int(a[index]) - int(b[index])
		}
	}
	return 0
}

// sweep runs fping over the target list and returns the alive addresses.
func (s *Service) sweep(ctx context.Context, targets []scanTarget, timeoutMS int) (map[string]bool, error) {
	binary, err := exec.LookPath("fping")
	if err != nil {
		return nil, errors.New("fping is not installed on the Atlas host; live-IP scan is unavailable")
	}
	file, err := os.CreateTemp("", "atlas-scan-*.txt")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name())
	writer := bufio.NewWriter(file)
	for _, target := range targets {
		if _, err := writer.WriteString(target.ip + "\n"); err != nil {
			file.Close()
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return nil, err
	}
	file.Close()

	// fping probes many hosts concurrently, so the wall clock is far below the
	// serial worst case. Budget generously but keep a floor so a small sweep is
	// never cut short by scheduling jitter.
	overall := time.Duration(len(targets)) * time.Duration(timeoutMS) * time.Millisecond / 50
	if overall < 5*time.Minute {
		overall = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, overall)
	defer cancel()
	command := exec.CommandContext(runCtx, binary, "-a", "-r", "1", "-t", fmt.Sprintf("%d", timeoutMS), "-f", file.Name())
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		// fping exits 1 when at least one target did not answer, which is the
		// normal case for a sweep; only treat other failures as errors.
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil, fmt.Errorf("fping sweep failed: %w", err)
		}
	}
	alive := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if net.ParseIP(fields[0]) != nil {
			alive[fields[0]] = true
		}
	}
	return alive, nil
}

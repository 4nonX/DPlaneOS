package hardware

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// SystemProfile contains detected hardware information
type SystemProfile struct {
	// CPU
	CPUVendor      string
	CPUModel       string
	CPUCores       int
	CPUThreads     int
	
	// Memory
	TotalRAM       uint64
	TotalRAMGB     int
	HasECC         bool
	
	// Storage
	HasZFS         bool
	HasBtrfs       bool
	PrimaryDiskType string
	
	// Platform
	Architecture   string
	IsVirtualized  bool
	VirtType       string
	
	// Kernel
	KernelVersion  string
	MaxInotify     int
	
	// Recommendations
	RecommendedARC     uint64
	RecommendedInotify int
	RecommendedWorkers int
}

// DetectHardware analyzes the system and returns a profile
func DetectHardware() (*SystemProfile, error) {
	profile := &SystemProfile{
		Architecture: runtime.GOARCH,
	}
	
	detectCPU(profile)
	detectMemory(profile)
	detectStorage(profile)
	detectVirtualization(profile)
	detectKernel(profile)
	calculateRecommendations(profile)
	
	return profile, nil
}

func detectCPU(p *SystemProfile) {
	p.CPUThreads = runtime.NumCPU()
	
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		p.CPUCores = p.CPUThreads
		return
	}
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	coresMap := make(map[string]bool)
	
	for scanner.Scan() {
		line := scanner.Text()
		
		if strings.HasPrefix(line, "vendor_id") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				vendor := strings.TrimSpace(parts[1])
				if strings.Contains(vendor, "Intel") {
					p.CPUVendor = "Intel"
				} else if strings.Contains(vendor, "AMD") {
					p.CPUVendor = "AMD"
				} else if strings.Contains(vendor, "ARM") {
					p.CPUVendor = "ARM"
				}
			}
		}
		
		if strings.HasPrefix(line, "model name") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				p.CPUModel = strings.TrimSpace(parts[1])
			}
		}
		
		if strings.HasPrefix(line, "core id") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 {
				coreID := strings.TrimSpace(parts[1])
				coresMap[coreID] = true
			}
		}
	}
	
	p.CPUCores = len(coresMap)
	if p.CPUCores == 0 {
		p.CPUCores = p.CPUThreads
	}
}

func detectMemory(p *SystemProfile) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer file.Close()
	
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		
		if strings.HasPrefix(line, "MemTotal:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				kb, err := strconv.ParseUint(parts[1], 10, 64)
				if err == nil {
					p.TotalRAM = kb * 1024
					p.TotalRAMGB = int(kb / 1024 / 1024)
					if kb%1048576 >= 524288 {
						p.TotalRAMGB++
					}
				}
			}
			break
		}
	}
	
	p.HasECC = detectECC()
}

func detectECC() bool {
	cmd := exec.Command("dmidecode", "-t", "memory")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	
	outStr := string(output)
	return strings.Contains(outStr, "Error Correction Type:") && 
	       !strings.Contains(outStr, "Error Correction Type: None")
}

func detectStorage(p *SystemProfile) {
	_, err := exec.LookPath("zpool")
	p.HasZFS = (err == nil)
	
	_, err = exec.LookPath("btrfs")
	p.HasBtrfs = (err == nil)
	
	cmd := exec.Command("lsblk", "-d", "-o", "NAME,ROTA")
	output, err := cmd.Output()
	if err != nil {
		p.PrimaryDiskType = "unknown"
		return
	}
	
	lines := strings.Split(string(output), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name := fields[0]
			rota := fields[1]
			
			if strings.HasPrefix(name, "nvme") {
				p.PrimaryDiskType = "NVMe"
				return
			} else if rota == "0" {
				p.PrimaryDiskType = "SSD"
				return
			} else if rota == "1" {
				p.PrimaryDiskType = "HDD"
				return
			}
		}
	}
	
	p.PrimaryDiskType = "unknown"
}

func detectVirtualization(p *SystemProfile) {
	cmd := exec.Command("systemd-detect-virt")
	output, err := cmd.Output()
	if err != nil {
		p.IsVirtualized = false
		p.VirtType = "none"
		return
	}
	
	virt := strings.TrimSpace(string(output))
	if virt == "none" {
		p.IsVirtualized = false
		p.VirtType = "none"
	} else {
		p.IsVirtualized = true
		p.VirtType = virt
	}
}

func detectKernel(p *SystemProfile) {
	cmd := exec.Command("uname", "-r")
	output, err := cmd.Output()
	if err != nil {
		p.KernelVersion = "3.2.0"
	} else {
		p.KernelVersion = strings.TrimSpace(string(output))
	}
	
	data, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		p.MaxInotify = 8192
		return
	}
	
	val, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		p.MaxInotify = 8192
		return
	}
	
	p.MaxInotify = val
}

func calculateRecommendations(p *SystemProfile) {
	// ZFS ARC
	if p.TotalRAMGB >= 128 {
		p.RecommendedARC = 32 * 1024 * 1024 * 1024
	} else if p.TotalRAMGB >= 64 {
		p.RecommendedARC = 16 * 1024 * 1024 * 1024
	} else if p.TotalRAMGB >= 32 {
		p.RecommendedARC = 8 * 1024 * 1024 * 1024
	} else if p.TotalRAMGB >= 16 {
		if p.HasECC {
			p.RecommendedARC = 6 * 1024 * 1024 * 1024
		} else {
			p.RecommendedARC = 4 * 1024 * 1024 * 1024
		}
	} else if p.TotalRAMGB >= 8 {
		p.RecommendedARC = 2 * 1024 * 1024 * 1024
	} else if p.TotalRAMGB >= 4 {
		p.RecommendedARC = 1 * 1024 * 1024 * 1024
	} else {
		p.RecommendedARC = 512 * 1024 * 1024
	}
	
	// Inotify
	if p.TotalRAMGB >= 32 {
		p.RecommendedInotify = 1048576
	} else if p.TotalRAMGB >= 16 {
		p.RecommendedInotify = 524288
	} else if p.TotalRAMGB >= 8 {
		p.RecommendedInotify = 262144
	} else if p.TotalRAMGB >= 4 {
		p.RecommendedInotify = 131072
	} else {
		p.RecommendedInotify = 65536
	}
	
	// Workers
	if p.CPUCores >= 16 {
		p.RecommendedWorkers = 8
	} else if p.CPUCores >= 8 {
		p.RecommendedWorkers = 4
	} else if p.CPUCores >= 4 {
		p.RecommendedWorkers = 2
	} else {
		p.RecommendedWorkers = 1
	}
	
	// Adjust for virtualization
	if p.IsVirtualized {
		p.RecommendedARC = p.RecommendedARC * 3 / 4
		p.RecommendedInotify = p.RecommendedInotify / 2
	}
}

func (p *SystemProfile) String() string {
	ecc := "No"
	if p.HasECC {
		ecc = "Yes"
	}
	
	virt := "Bare Metal"
	if p.IsVirtualized {
		virt = fmt.Sprintf("Virtualized (%s)", p.VirtType)
	}
	
	return fmt.Sprintf(`System Profile:
  CPU: %s %s (%d cores, %d threads)
  RAM: %d GB (ECC: %s)
  Storage: %s (ZFS: %v, Btrfs: %v)
  Platform: %s, %s
  Kernel: %s
  
Recommendations:
  ZFS ARC: %d MB
  Inotify Watches: %d
  Worker Threads: %d`,
		p.CPUVendor, p.CPUModel, p.CPUCores, p.CPUThreads,
		p.TotalRAMGB, ecc,
		p.PrimaryDiskType, p.HasZFS, p.HasBtrfs,
		p.Architecture, virt,
		p.KernelVersion,
		p.RecommendedARC/1024/1024,
		p.RecommendedInotify,
		p.RecommendedWorkers)
}

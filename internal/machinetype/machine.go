// Package machinetype provides a best-effort, process-wide hardware label.
package machinetype

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var local = sync.OnceValue(detect)

func Local() string { return local() }
func Normalize(kind string) string {
	switch kind {
	case "laptop", "desktop", "cloud":
		return kind
	}
	return "server"
}
func apple(model string) string {
	model = strings.ToLower(model)
	if strings.Contains(model, "macbook") {
		return "laptop"
	}
	for _, name := range []string{"macmini", "mac mini", "macstudio", "mac studio", "imac", "macpro", "mac pro"} {
		if strings.Contains(model, name) {
			return "desktop"
		}
	}
	return "server"
}
func probe(command string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, command, args...).Output()
	return strings.TrimSpace(string(out))
}
func detect() string {
	switch runtime.GOOS {
	case "darwin":
		if kind := apple(probe("ioreg", "-rd1", "-n", "product")); kind != "server" {
			return kind
		}
		return apple(probe("sysctl", "-n", "hw.model"))
	case "linux":
		read := func(name string) string {
			b, _ := os.ReadFile("/sys/class/dmi/id/" + name)
			return strings.TrimSpace(string(b))
		}
		return linux(read("sys_vendor")+" "+read("product_name"), read("chassis_type"))
	case "windows":
		chassis := probe("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_SystemEnclosure).ChassisTypes -join ','")
		return linux(probe("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-CimInstance Win32_ComputerSystem).Model"), chassis)
	}
	return "server"
}
func linux(product, chassis string) string {
	product = strings.ToLower(product)
	for _, marker := range []string{"qemu", "kvm", "vmware", "virtualbox", "virtual machine", "amazon ec2", "google compute", "openstack", "parallels", "digitalocean"} {
		if strings.Contains(product, marker) {
			return "cloud"
		}
	}
	for _, c := range strings.Split(chassis, ",") {
		switch strings.TrimSpace(c) {
		case "8", "9", "10", "14", "31", "32":
			return "laptop"
		case "3", "4", "5", "6", "7", "13", "15", "16", "35":
			return "desktop"
		}
	}
	return "server"
}

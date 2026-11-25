package layout

import (
	"embed"
	"github.com/a-h/templ"
)

//go:embed machineicons/*.svg
var machineIcons embed.FS

// MachineIcon renders only embedded, trusted Tabler artwork.
func MachineIcon(kind string) templ.Component {
	name := "server"
	switch kind {
	case "laptop":
		name = "device-laptop"
	case "desktop":
		name = "device-desktop"
	case "cloud":
		name = "cloud"
	}
	data, _ := machineIcons.ReadFile("machineicons/" + name + ".svg")
	return templ.Raw(string(data))
}

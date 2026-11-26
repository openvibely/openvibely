package layout

import (
	"embed"
	"encoding/json"
	"github.com/a-h/templ"
)

// ServerIconChoice describes a bundled Tabler icon. Categories may overlap.
type ServerIconChoice struct{ Name, Label, Categories string }

//go:embed servericons/*.svg servericons/catalog.json
var serverIcons embed.FS
var serverIconChoices = func() []ServerIconChoice {
	data, _ := serverIcons.ReadFile("servericons/catalog.json")
	var choices []ServerIconChoice
	if err := json.Unmarshal(data, &choices); err != nil {
		panic(err)
	}
	return choices
}()

func ServerIconChoices() []ServerIconChoice {
	return append([]ServerIconChoice(nil), serverIconChoices...)
}
func ValidServerIcon(name string) bool {
	if name == "" {
		return true
	}
	for _, c := range serverIconChoices {
		if c.Name == name {
			return true
		}
	}
	return false
}
func ServerIcon(name, machine string) templ.Component {
	if name != "" && ValidServerIcon(name) {
		data, err := serverIcons.ReadFile("servericons/" + name + ".svg")
		if err == nil {
			return templ.Raw(string(data))
		}
	}
	return MachineIcon(machine)
}

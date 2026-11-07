package templates

import (
	_ "embed"

	"github.com/a-h/templ"
)

//go:embed menu.css
var menuCSS string

//go:embed menu_controller.js
var menuControllerJS string

//go:embed menu_backdrop.js
var menuBackdropJS string

// Share menu styling and the pointer boundary, including standalone HTMX fragments.
var menuStyles = templ.NewOnceHandle(templ.WithComponent(templ.Raw("<style data-ov-menu-styles>" + menuCSS + "</style><script>" + menuControllerJS + menuBackdropJS + "</script>")))

func MenuStyles() templ.Component { return menuStyles.Once() }

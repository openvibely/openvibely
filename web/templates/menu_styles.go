package templates

import (
	_ "embed"

	"github.com/a-h/templ"
)

//go:embed menu.css
var menuCSS string

// Share one stylesheet per render, including standalone HTMX fragments.
var menuStyles = templ.NewOnceHandle(templ.WithComponent(templ.Raw("<style data-ov-menu-styles>" + menuCSS + "</style>")))

func MenuStyles() templ.Component { return menuStyles.Once() }

package templates

type SearchableSelectorConfig struct {
	CreateURL     string
	CreateLabel   string
	ID            string
	Kind          string
	SearchURL     string
	Local         bool
	InitialStatus string
}

const (
	SearchableSelectorDialogClass      = "fixed m-0 max-h-[min(32rem,calc(100dvh-1rem))] w-[28rem] max-w-[calc(100vw-1rem)] overflow-hidden ov-menu p-0 backdrop:bg-transparent"
	SearchableSelectorPanelClass       = "flex max-h-[inherit] min-h-0 flex-col overflow-hidden"
	SearchableSelectorSearchShellClass = "ov-menu-search"
	SearchableSelectorSearchClass      = "w-full"
	SearchableSelectorResultsClass     = "ov-menu-list ov-menu-scroll min-h-0 flex-1 overscroll-contain"
	SearchableSelectorMenuClass        = "w-full min-w-0 p-0"
	SearchableSelectorOptionClass      = "ov-menu-option w-full max-w-full overflow-hidden"
)

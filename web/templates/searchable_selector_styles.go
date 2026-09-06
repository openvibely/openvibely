package templates

type SearchableSelectorConfig struct {
	ID            string
	Kind          string
	SearchURL     string
	Local         bool
	InitialStatus string
}

const (
	SearchableSelectorDialogClass      = "fixed m-0 max-h-[min(32rem,calc(100dvh-1rem))] w-[28rem] max-w-[calc(100vw-1rem)] overflow-hidden rounded-box border border-base-300 bg-base-100 p-0 text-base-content shadow-xl backdrop:bg-transparent"
	SearchableSelectorPanelClass       = "flex max-h-[inherit] min-h-0 flex-col overflow-hidden"
	SearchableSelectorSearchShellClass = "card border border-base-300 bg-base-100 shadow-sm"
	SearchableSelectorSearchClass      = "w-full border-0 bg-transparent px-4 py-2 text-sm focus:outline-none focus:ring-0"
	SearchableSelectorResultsClass     = "min-h-0 flex-1 overflow-y-auto overscroll-contain p-2"
	SearchableSelectorMenuClass        = "menu w-full gap-1 p-0"
	SearchableSelectorOptionClass      = "flex min-w-0 items-center gap-2 rounded-btn px-4 py-2 hover:bg-base-content/10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
)

package persistence

type ChannelTargetProjectSummary struct {
	Total      int
	Configured bool
	ByPlatform map[string]ChannelTargetPlatformSummary
}

type ChannelTargetPlatformSummary struct {
	Total  int
	Home   int
	Named  int
	ByKind map[string]int
}

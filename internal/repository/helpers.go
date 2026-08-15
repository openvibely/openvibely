package repository

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

package repository

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// normalizeCardPageArgs bounds fetches used by card-list pagination. Callers
// request one extra row to determine whether another page exists.
func normalizeCardPageArgs(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 21
	}
	if limit > 51 {
		limit = 51
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

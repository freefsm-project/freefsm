package services

func PaginationOffset(page, perPage int) int {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		return 0
	}
	return (page - 1) * perPage
}

func TotalPages(total, perPage int) int {
	if perPage < 1 {
		return 1
	}
	pages := (total + perPage - 1) / perPage
	if pages < 1 {
		return 1
	}
	return pages
}

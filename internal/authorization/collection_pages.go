package authorization

// Collection pages deliberately retain their domain DTOs. They do not reuse
// discovery's restricted result identity projection or the unbounded export
// selectors. Count and bounded page are read under WithinSnapshotPermission.
func collectionPageBounds(page, size int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if size == 0 {
		size = 25
	}
	if page < 1 || page > 1000000 || size != 25 && size != 50 && size != 100 {
		return 0, 0, ErrConflict
	}
	return size, (page - 1) * size, nil
}

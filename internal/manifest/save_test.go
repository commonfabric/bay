package manifest

// save replaces the manifest file at path with m, under the manifest's lock.
func save(path string, m *Manifest) error {
	return LockedUpdate(path, func(cur *Manifest) error {
		*cur = *m
		return nil
	})
}

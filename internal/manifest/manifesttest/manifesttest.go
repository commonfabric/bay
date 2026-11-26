// Package manifesttest writes manifest files for tests.
package manifesttest

import "github.com/commonfabric/bay/internal/manifest"

// Save replaces the manifest file at path with m, under the manifest's lock.
func Save(path string, m *manifest.Manifest) error {
	return manifest.LockedUpdate(path, func(cur *manifest.Manifest) error {
		*cur = *m
		return nil
	})
}

// Package privacy decides which profiles may see a library track.
package privacy

import (
	"path/filepath"
	"strings"
)

// PrivateDir is the library folder that holds per-profile music. A track at
// <library>/_private/<profile id>/... belongs to that profile alone; every
// other track is shared. The Python worker applies the same rule
// (musik.privacy) when it builds mixes, so keep the two in step.
const PrivateDir = "_private"

// PrivateOwner returns the profile that owns path, or "" for a shared track.
// It matches the path segment rather than a library prefix, so the player
// and the worker agree even when they mount the library at different paths.
func PrivateOwner(path string) string {
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == PrivateDir && parts[i+1] != "" {
			return parts[i+1]
		}
	}
	return ""
}

// VisibleTo reports whether profileID may see the track at path.
func VisibleTo(path, profileID string) bool {
	owner := PrivateOwner(path)
	return owner == "" || owner == profileID
}

// PrivateRoot is the folder for profileID's own music under root.
func PrivateRoot(root, profileID string) string {
	return filepath.Join(root, PrivateDir, profileID)
}

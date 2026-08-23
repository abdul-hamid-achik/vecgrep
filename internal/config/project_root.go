package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNestedProjectBoundary is returned when cwd sits inside a registered ancestor
// but looks like its own codebase root (e.g. a git repo under a monorepo folder).
var ErrNestedProjectBoundary = fmt.Errorf("nested project boundary")

// projectRootMarkers are files/dirs that mark a vecgrep project root when present
// directly in a directory (checked before walking to parents).
var projectRootMarkers = []string{
	"vecgrep.yaml",
	"vecgrep.yml",
	filepath.Join(".config", "vecgrep.yaml"),
	DefaultDataDir,
}

// projectBoundaryMarkers are signals that a directory is a standalone codebase
// root nested inside a broader registered ancestor.
var projectBoundaryMarkers = []string{
	".git",
	"go.mod",
	"package.json",
	"pyproject.toml",
	"Cargo.toml",
	"composer.json",
	"Gemfile",
}

// FindDeepestRegisteredProjectContaining returns the globally registered project
// with the longest path that contains dir (equal or descendant). This prefers a
// child registration like pasaprecio over a parent like ~/projects.
func FindDeepestRegisteredProjectContaining(dir string) (string, *ProjectEntry, bool) {
	absDir, err := absClean(dir)
	if err != nil {
		return "", nil, false
	}

	globalCfg, err := LoadGlobalConfig()
	if err != nil {
		return "", nil, false
	}

	var bestName string
	var bestEntry ProjectEntry
	bestLen := -1

	for name, entry := range globalCfg.Projects {
		entryPath, err := absClean(ExpandPath(entry.Path))
		if err != nil {
			continue
		}
		if !pathContains(entryPath, absDir) {
			continue
		}
		if len(entryPath) > bestLen {
			bestLen = len(entryPath)
			bestName = name
			bestEntry = entry
		}
	}

	if bestLen < 0 {
		return "", nil, false
	}
	return bestName, &bestEntry, true
}

// NestedProjectBoundary walks from startDir up toward ancestorRoot (exclusive) and
// returns the deepest directory that LooksLikeProjectRoot. When startDir itself
// is a boundary, boundary equals startDir.
func NestedProjectBoundary(startDir, ancestorRoot string) (boundary string, ok bool) {
	absStart, err := absClean(startDir)
	if err != nil {
		return "", false
	}
	absAncestor, err := absClean(ancestorRoot)
	if err != nil {
		return "", false
	}
	if !pathContains(absAncestor, absStart) {
		return "", false
	}

	dir := absStart
	for {
		if pathsEqual(dir, absAncestor) {
			return "", false
		}
		if LooksLikeProjectRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// LooksLikeProjectRoot reports whether dir appears to be a standalone codebase root.
func LooksLikeProjectRoot(dir string) bool {
	for _, marker := range projectBoundaryMarkers {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	for _, marker := range projectRootMarkers {
		if marker == DefaultDataDir {
			if info, err := os.Stat(filepath.Join(dir, marker)); err == nil && info.IsDir() {
				globalDir, _ := GetGlobalConfigDir()
				if globalDir == "" || filepath.Join(dir, marker) != globalDir {
					return true
				}
			}
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, marker)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// AncestorRegisteredProject returns the deepest registered ancestor of dir when
// dir is not itself registered and no local marker was found. The second return
// is the registry name when an ancestor shadows a nested project boundary.
func AncestorRegisteredProject(dir string) (ancestorRoot, registryName string, shadowed bool) {
	name, entry, ok := FindDeepestRegisteredProjectContaining(dir)
	if !ok {
		return "", "", false
	}
	regRoot, err := absClean(ExpandPath(entry.Path))
	if err != nil {
		return "", "", false
	}
	absDir, err := absClean(dir)
	if err != nil {
		return "", "", false
	}
	if pathsEqual(absDir, regRoot) {
		return regRoot, name, false
	}
	if boundary, has := NestedProjectBoundary(absDir, regRoot); has && !pathsEqual(boundary, regRoot) {
		return regRoot, name, true
	}
	return regRoot, name, false
}

func findLocalProjectRootFrom(startDir string) (string, bool) {
	dir, err := absClean(startDir)
	if err != nil {
		return "", false
	}

	globalDir, _ := GetGlobalConfigDir()

	for {
		for _, marker := range projectRootMarkers {
			candidate := filepath.Join(dir, marker)
			if marker == DefaultDataDir {
				if info, err := os.Stat(candidate); err == nil && info.IsDir() {
					if globalDir == "" || candidate != globalDir {
						return dir, true
					}
				}
				continue
			}
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return dir, true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func absClean(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs, err := filepath.Abs(ExpandPath(path))
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func PathsEqual(a, b string) bool {
	return pathsEqual(a, b)
}

func pathsEqual(a, b string) bool {
	a, errA := absClean(a)
	b, errB := absClean(b)
	if errA != nil || errB != nil {
		return false
	}
	return a == b
}

// pathContains reports whether root equals child or is a strict ancestor of child.
func pathContains(root, child string) bool {
	root, errR := absClean(root)
	child, errC := absClean(child)
	if errR != nil || errC != nil {
		return false
	}
	if root == child {
		return true
	}
	rel, err := filepath.Rel(root, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

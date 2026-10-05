package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// A plugin unit is not one file: the declaration says what may be called, and the binary it names
// is what actually runs. Digesting only the declaration would mean an operator - or anybody with
// write access to the pack directory - could replace the executable after install and the table
// would still report the unit as un-drifted, which is exactly the question Drifted() exists to
// answer for every other kind.
//
// So for this kind the unit's fingerprint covers both files, in a fixed order. The extra path is
// derived from the declaration itself rather than passed in, because the two call sites that
// compute digests (install-time Resolve and Drifted) only ever have a unit path.

// pluginDeclarationDir is the directory a plugin declaration must live in, relative to the pack
// root. The rule makes "which pack owns this file" derivable from the path alone instead of
// guessed by walking up an arbitrary number of levels.
const pluginDeclarationDir = "plugins"

// DigestPaths fingerprints several files as one value, using the same per-file framing Digest()
// uses when a unit is a directory: the name each file contributes is part of the digest, so
// swapping which file holds which bytes is a change.
func DigestPaths(paths ...string) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("plugin: no paths to digest")
	}
	sum := sha256.New()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("plugin: %q must be a file for a multi-file digest", path)
		}
		if err := hashFile(sum, path, filepath.Base(path)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// PluginBinaryPath resolves the binary one plugin declaration names, contained in the pack that
// owns the declaration. Exported because the package that owns the declaration schema (the plug-in
// surface) has to agree with this derivation, and an agreement nobody can compare is a rule that
// quietly splits: TestPluginCompanionAgreesWithTheLoader is that comparison.
func PluginBinaryPath(declarationPath string) (string, error) {
	packDir, err := pluginPackDir(declarationPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(declarationPath)
	if err != nil {
		return "", err
	}
	var shape struct {
		Binary string `yaml:"binary"`
	}
	if err := yaml.Unmarshal(data, &shape); err != nil {
		return "", fmt.Errorf("plugin: cannot read the binary key of %s: %w", declarationPath, err)
	}
	declared := strings.TrimSpace(shape.Binary)
	if declared == "" {
		return "", fmt.Errorf("plugin: %s declares no binary", declarationPath)
	}
	if filepath.IsAbs(declared) {
		return "", fmt.Errorf("plugin: %s names an absolute binary %q", declarationPath, declared)
	}
	cleaned := filepath.Clean(declared)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin: %s escapes its pack with %q", declarationPath, declared)
	}
	candidate := filepath.Join(packDir, cleaned)
	// Containment is checked after resolving links on both sides, so a symbolic link inside a pack
	// cannot be a door out of it.
	resolvedPack, err := filepath.EvalSymlinks(packDir)
	if err != nil {
		return "", fmt.Errorf("plugin: pack directory %s is not usable: %w", packDir, err)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return "", fmt.Errorf("plugin: %s names an unreadable binary: %w", declarationPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("plugin: %s names a directory as its binary", declarationPath)
	}
	resolvedBinary, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("plugin: cannot resolve %s: %w", candidate, err)
	}
	if inside, err := filepath.Rel(resolvedPack, resolvedBinary); err != nil ||
		inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("plugin: %s resolves its binary outside the pack", declarationPath)
	}
	return resolvedBinary, nil
}

// pluginPackDir is the pack root inferred from a declaration path: <pack>/plugins/<name>.yaml.
func pluginPackDir(declarationPath string) (string, error) {
	dir := filepath.Dir(declarationPath)
	if filepath.Base(dir) != pluginDeclarationDir {
		return "", fmt.Errorf("plugin: declarations must live in %s/, got %s", pluginDeclarationDir, declarationPath)
	}
	return filepath.Dir(dir), nil
}

// companionOf returns the files that additionally define a unit. Only the code-bearing kind has
// any: role/agent/tool/mcp declarations are the whole capability, and a skill is already hashed
// as a directory.
func companionOf(kind Kind, unitPath string) ([]string, error) {
	if kind != KindPlugin {
		return nil, nil
	}
	binary, err := PluginBinaryPath(unitPath)
	if err != nil {
		return nil, err
	}
	return []string{binary}, nil
}

// digestUnit is the one way a unit's fingerprint is computed, at install and at drift check alike.
// A kind with companions that failed to resolve is reported rather than downgraded to a
// declaration-only digest: silently hashing less is how drift detection starts lying.
func digestUnit(kind Kind, unitPath string) (string, error) {
	companions, err := companionOf(kind, unitPath)
	if err != nil {
		return "", err
	}
	if len(companions) == 0 {
		return Digest(unitPath)
	}
	return DigestPaths(append([]string{unitPath}, companions...)...)
}

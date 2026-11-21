package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Neon-Dolls/neondoll/Core/Skill"
)

func resolveRoot(dataRoot, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", skill.ErrInvalidSkill
	}
	if filepath.IsAbs(configured) {
		root, err := filepath.EvalSymlinks(filepath.Clean(configured))
		if err != nil {
			return "", fmt.Errorf("skill local: resolve absolute directory: %w", err)
		}
		return requireDirectory(root)
	}
	if strings.TrimSpace(dataRoot) == "" {
		return "", fmt.Errorf("%w: data root required for relative Skill directory", skill.ErrInvalidSkill)
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("skill local: resolve data root: %w", err)
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.Clean(configured)))
	if err != nil || !within(root, target) {
		return "", fmt.Errorf("%w: Skill directory escapes data root", skill.ErrInvalidSkill)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil || !within(root, resolved) {
		return "", fmt.Errorf("%w: Skill directory symlink escapes data root", skill.ErrInvalidSkill)
	}
	return requireDirectory(resolved)
}

func requireDirectory(root string) (string, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: configured Skill directory is unavailable", skill.ErrInvalidSkill)
	}
	return root, nil
}

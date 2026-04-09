package version

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const FileName = "VERSION"

func Read(root string) (string, error) {
	content, err := os.ReadFile(filepath.Join(root, FileName))
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", fmt.Errorf("%s is missing a version", FileName)
	}
	return strings.TrimPrefix(value, "v"), nil
}

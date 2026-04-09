package gocliassets

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed generated/*.json
var generatedAssets embed.FS

var (
	loadOnce      sync.Once
	loadErr       error
	promptFiles   map[string]string
	skillFiles    map[string]string
	templateFiles map[string]string
)

func Prompts() (map[string]string, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	return cloneMap(promptFiles), nil
}

func Skills() (map[string]string, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	return cloneMap(skillFiles), nil
}

func Templates() (map[string]string, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	return cloneMap(templateFiles), nil
}

func load() {
	loadOnce.Do(func() {
		promptFiles, loadErr = readJSONMap("generated/prompts.json")
		if loadErr != nil {
			return
		}
		skillFiles, loadErr = readJSONMap("generated/skills.json")
		if loadErr != nil {
			return
		}
		templateFiles, loadErr = readJSONMap("generated/templates.json")
	})
}

func readJSONMap(path string) (map[string]string, error) {
	content, err := generatedAssets.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read embedded asset %s: %w", path, err)
	}
	var values map[string]string
	if err := json.Unmarshal(content, &values); err != nil {
		return nil, fmt.Errorf("parse embedded asset %s: %w", path, err)
	}
	return values, nil
}

func cloneMap(input map[string]string) map[string]string {
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

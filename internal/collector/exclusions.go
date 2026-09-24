package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ExclusionEntry struct {
	Name       string `json:"name"`
	Catalog    string `json:"catalog,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ExcludedAt string `json:"excludedAt"`
}

type Exclusions struct {
	Operators []ExclusionEntry `json:"operators,omitempty"`
	Images    []ExclusionEntry `json:"images,omitempty"`
}

func LoadExclusions(dir string) (*Exclusions, error) {
	path := filepath.Join(dir, "exclusions.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return &Exclusions{}, nil
	}
	var ex Exclusions
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, fmt.Errorf("exclusions.json のパースに失敗: %w", err)
	}
	return &ex, nil
}

func (e *Exclusions) Save(dir string) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "exclusions.json")
	return os.WriteFile(path, data, 0644)
}

func (e *Exclusions) ExcludeOperator(name, catalog, reason string) bool {
	for _, op := range e.Operators {
		if op.Name == name {
			return false
		}
	}
	e.Operators = append(e.Operators, ExclusionEntry{
		Name:       name,
		Catalog:    catalog,
		Reason:     reason,
		ExcludedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return true
}

func (e *Exclusions) ExcludeImage(name, reason string) bool {
	for _, img := range e.Images {
		if img.Name == name {
			return false
		}
	}
	e.Images = append(e.Images, ExclusionEntry{
		Name:       name,
		Reason:     reason,
		ExcludedAt: time.Now().UTC().Format(time.RFC3339),
	})
	return true
}

func (e *Exclusions) IncludeOperator(name string) bool {
	for i, op := range e.Operators {
		if op.Name == name {
			e.Operators = append(e.Operators[:i], e.Operators[i+1:]...)
			return true
		}
	}
	return false
}

func (e *Exclusions) IncludeImage(name string) bool {
	for i, img := range e.Images {
		if img.Name == name {
			e.Images = append(e.Images[:i], e.Images[i+1:]...)
			return true
		}
	}
	return false
}

func (e *Exclusions) IsOperatorExcluded(name string) (bool, string) {
	for _, op := range e.Operators {
		if op.Name == name {
			return true, op.Reason
		}
	}
	return false, ""
}

func (e *Exclusions) IsImageExcluded(name string) (bool, string) {
	for _, img := range e.Images {
		if img.Name == name {
			return true, img.Reason
		}
	}
	return false, ""
}

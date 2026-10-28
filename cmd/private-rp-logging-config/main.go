// private-rp-logging-config enables the explicitly provisioned local log volume.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

func prepare(data []byte) ([]byte, error) {
	normalized, _, err := config.NormalizeConfigLayout(data, true)
	if err != nil {
		return nil, fmt.Errorf("normalize configuration: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(normalized, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("invalid configuration document")
	}
	values := []struct {
		path  []string
		tag   string
		value string
	}{
		{[]string{"observability", "logs", "logging-to-file"}, "!!bool", "true"},
		{[]string{"observability", "logs", "request-log"}, "!!bool", "true"},
		{[]string{"observability", "logs", "logs-max-total-size-mb"}, "!!int", "18432"},
		{[]string{"management", "disable-auto-update-panel"}, "!!bool", "true"},
	}
	for _, setting := range values {
		if err := setScalar(doc.Content[0], setting.path, setting.tag, setting.value); err != nil {
			return nil, err
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	if _, err := config.ParseConfigBytes(output.Bytes()); err != nil {
		return nil, fmt.Errorf("validate prepared configuration: %w", err)
	}
	return output.Bytes(), nil
}

func setScalar(root *yaml.Node, path []string, tag, value string) error {
	for i, key := range path {
		if root.Kind != yaml.MappingNode {
			return fmt.Errorf("configuration path is not a mapping")
		}
		var next *yaml.Node
		for j := 0; j+1 < len(root.Content); j += 2 {
			if root.Content[j].Value == key {
				next = root.Content[j+1]
				break
			}
		}
		if next == nil {
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, next)
		}
		if i == len(path)-1 {
			next.Kind, next.Tag, next.Value = yaml.ScalarNode, tag, value
			next.Content = nil
			return nil
		}
		root = next
	}
	return nil
}

func run(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	prepared, err := prepare(data)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".private-log-config-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(prepared); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: private-rp-logging-config CONFIG_PATH")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "failed to prepare private logging configuration")
		os.Exit(1)
	}
}

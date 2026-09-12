package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// runCommand executes every scenario for both languages and prints the
// report as JSON.
func runCommand(arguments []string) error {
	label := "unlabelled"
	for index := 0; index < len(arguments); index++ {
		switch arguments[index] {
		case "--label":
			if index+1 >= len(arguments) {
				return errors.New("--label needs a value")
			}
			label = arguments[index+1]
			index++
		default:
			return fmt.Errorf("unknown option %q", arguments[index])
		}
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	encoder, err := tokenizer(root)
	if err != nil {
		return err
	}
	s, err := startSession(root, encoder)
	if err != nil {
		return err
	}
	defer s.close()
	for _, spec := range specs {
		s.runHuyang(spec)
		s.runModelled(spec)
	}
	features := make([]string, 0, len(s.features))
	for name, present := range s.features {
		if present {
			features = append(features, name)
		}
	}
	report := Report{Label: label, Huyang: huyangVersion(root), Tokenizer: "cl100k_base (tiktoken-go)", Calls: s.calls,
		Notes: append(s.notes, "catalog features: "+strings.Join(sorted(features), ", "))}
	encoded, err := json.MarshalIndent(report, "", " ")
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(encoded, '\n'))
	return err
}

func sorted(values []string) []string {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
	return values
}

func huyangVersion(root string) string {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	version := strings.TrimSpace(string(output))
	if status, statusErr := exec.Command("git", "-C", root, "status", "--porcelain", "--", filepath.Join("internal"), filepath.Join("lua")).Output(); statusErr == nil && len(status) > 0 {
		version += "+dirty"
	}
	return version
}

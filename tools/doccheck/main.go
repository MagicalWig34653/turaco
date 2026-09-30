package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var linkPattern = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

func main() {
	files := []string{"README.md", "CONTRIBUTING.md", "SECURITY.md", "CLAUDE.md"}
	_ = filepath.WalkDir("docs", func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return err
	})
	var failures []string
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		scanner := bufio.NewScanner(f)
		line := 0
		for scanner.Scan() {
			line++
			for _, match := range linkPattern.FindAllStringSubmatch(scanner.Text(), -1) {
				target := match[1]
				if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
					continue
				}
				target = strings.Split(target, "#")[0]
				if target == "" {
					continue
				}
				resolved := filepath.Clean(filepath.Join(filepath.Dir(file), filepath.FromSlash(target)))
				if _, err := os.Stat(resolved); err != nil {
					failures = append(failures, fmt.Sprintf("%s:%d broken link %q -> %s", file, line, target, resolved))
				}
			}
		}
		if err := scanner.Err(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", file, err))
		}
		_ = f.Close()
	}
	if len(failures) > 0 {
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, failure)
		}
		os.Exit(1)
	}
	fmt.Printf("documentation check: ok (%d markdown files)\n", len(files))
}

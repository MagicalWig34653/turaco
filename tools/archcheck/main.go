package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var moduleImport = regexp.MustCompile(`/backend/internal/modules/([^/]+)(/.*)?$`)

func main() {
	root := "backend/internal"
	var violations []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return err
		}
		owner, kind := ownerFor(path)
		auditAliases := map[string]bool{}
		for _, imp := range file.Imports {
			value := strings.Trim(imp.Path.Value, `"`)
			if strings.HasSuffix(value, "/backend/internal/platform/audit") {
				alias := "audit"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				if alias == "." {
					violations = append(violations, fmt.Sprintf("%s: dot import of audit hides WithAssist callers", path))
				} else if alias != "_" {
					auditAliases[alias] = true
				}
			}
			match := moduleImport.FindStringSubmatch(value)
			if match == nil {
				if kind == "platform" && strings.Contains(value, "/backend/internal/integrations/") {
					violations = append(violations, fmt.Sprintf("%s: platform package imports integration adapter %s (define a port and wire the adapter in the composition root)", path, value))
				}
				if kind == "domain" && (value == "net/http" || strings.HasPrefix(value, "github.com/jackc/pgx")) {
					violations = append(violations, fmt.Sprintf("%s: domain package imports infrastructure dependency %s", path, value))
				}
				continue
			}
			target := match[1]
			rest := match[2]
			if kind == "platform" {
				violations = append(violations, fmt.Sprintf("%s: platform package imports business module %s", path, target))
				continue
			}
			if owner != "" && owner != target && !strings.HasPrefix(rest, "/public") {
				violations = append(violations, fmt.Sprintf("%s: module %s imports private package of module %s (%s)", path, owner, target, value))
			}
		}
		violations = append(violations, assistViolations(path, file, set, auditAliases)...)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "architecture check:", err)
		os.Exit(1)
	}
	if len(violations) > 0 {
		for _, violation := range violations {
			fmt.Fprintln(os.Stderr, "ARCHITECTURE VIOLATION:", violation)
		}
		os.Exit(1)
	}
	fmt.Println("architecture check: ok")
}

func assistViolations(path string, file *ast.File, set *token.FileSet, aliases map[string]bool) []string {
	if filepath.ToSlash(filepath.Dir(path)) == "backend/internal/platform/ai" {
		return nil
	}
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithAssist" {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if ok && aliases[id.Name] {
			violations = append(violations, fmt.Sprintf("%s: only platform/ai may call audit.WithAssist", set.Position(sel.Pos())))
		}
		return true
	})
	return violations
}

func ownerFor(path string) (owner, kind string) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, p := range parts {
		if p == "platform" && i >= 1 && parts[i-1] == "internal" {
			return "", "platform"
		}
		if p == "modules" && i+1 < len(parts) {
			owner = parts[i+1]
			for _, part := range parts[i+2:] {
				if part == "domain" {
					return owner, "domain"
				}
			}
			return owner, "module"
		}
		if p == "integrations" {
			return "integration", "integration"
		}
	}
	return "", "other"
}

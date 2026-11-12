package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func allowed(text string) bool {
	return strings.HasPrefix(text, "//go:") || strings.HasPrefix(text, "// Code generated ")
}

func skipDir(name string) bool {
	return name == "vendor" || name == "testdata" || (strings.HasPrefix(name, ".") && name != ".")
}

func checkSource(fset *token.FileSet, path string, src any) ([]string, error) {
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []string
	for _, group := range f.Comments {
		for _, c := range group.List {
			if !allowed(c.Text) {
				out = append(out, fmt.Sprintf("%s: comment not allowed", fset.Position(c.Pos())))
			}
		}
	}
	return out, nil
}

func scan(root string) ([]string, error) {
	fset := token.NewFileSet()
	var violations []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && skipDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		v, err := checkSource(fset, path, nil)
		violations = append(violations, v...)
		return err
	})
	if err != nil {
		return violations, fmt.Errorf("scan %s: %w", root, err)
	}
	return violations, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	roots := args
	if len(roots) == 0 {
		roots = []string{"."}
	}
	total := 0
	for _, root := range roots {
		violations, err := scan(root)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		for _, v := range violations {
			fmt.Fprintln(stderr, v)
		}
		total += len(violations)
	}
	if total > 0 {
		fmt.Fprintf(stderr, "nocomments: %d comment(s) found\n", total)
		return 1
	}
	fmt.Fprintln(stdout, "nocomments: 0 comments")
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

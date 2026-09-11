package main

import (
	"go/build"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// ARCHITECTURE.md draws the package graph by hand, because a drawn graph can
// say which edge is a seam and which is a smell. A drawn graph also rots. This
// derives the real graph and compares, so the drawing cannot fall behind the
// code without the gate saying so.
//
// The contract, stated in the document's last section: the flowchart marked
// "%% verified: package graph", solid "-->" edges only, node ids being the
// last path segment of the package and "main" for the module root, and an
// edge written in a "%%" comment counting as declared but not drawn.
const moduleName = "wowinsight"

func TestArchitecturePackageGraphMatchesTheCode(t *testing.T) {
	doc, err := os.ReadFile("ARCHITECTURE.md")
	if err != nil {
		t.Fatalf("read ARCHITECTURE.md: %v", err)
	}
	drawn := drawnEdges(t, string(doc))
	real := realEdges(t)

	for _, e := range real {
		if !slices.Contains(drawn, e) {
			t.Errorf("the code has %s but ARCHITECTURE.md does not draw it — add the edge (and the package, if it is new)", e)
		}
	}
	for _, e := range drawn {
		if !slices.Contains(real, e) {
			t.Errorf("ARCHITECTURE.md draws %s but no such import exists — remove the edge, or make it dotted if it is not an import", e)
		}
	}
}

// drawnEdges returns the solid edges of the verified flowchart as "a --> b".
func drawnEdges(t *testing.T, doc string) []string {
	t.Helper()
	start := strings.Index(doc, "%% verified: package graph")
	if start < 0 {
		t.Fatal(`ARCHITECTURE.md has no flowchart marked "%% verified: package graph"`)
	}
	end := strings.Index(doc[start:], "```")
	if end < 0 {
		t.Fatal("the verified flowchart is not closed by a code fence")
	}
	block := doc[start : start+end]

	// A solid edge is "a --> b", optionally with a |label|, and optionally
	// behind a "%%" comment marker — declared for this test, not drawn. A
	// dotted edge is "a -.-> b" and is deliberately not matched: those are
	// the relations that are not imports.
	solid := regexp.MustCompile(`(?m)^\s*(?:%%\s*)?(\w+)\s*-->(?:\|[^|]*\|)?\s*(\w+)\s*$`)
	var edges []string
	for _, m := range solid.FindAllStringSubmatch(block, -1) {
		edges = append(edges, m[1]+" --> "+m[2])
	}
	if len(edges) == 0 {
		t.Fatal("the verified flowchart has no solid edges, so this test would assert nothing")
	}
	return edges
}

// realEdges walks the module for packages and returns their in-module import
// edges as "a --> b", using the same ids the document uses. Test-only imports
// are excluded: the diagram shows what ships.
func realEdges(t *testing.T) []string {
	t.Helper()
	var edges []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") && p != ".") {
			return fs.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		pkg, err := build.ImportDir(p, 0)
		if err != nil {
			if _, none := err.(*build.NoGoError); none {
				return nil
			}
			return err
		}
		from := nodeID(path.Join(moduleName, filepath.ToSlash(p)))
		for _, imp := range pkg.Imports {
			if strings.HasPrefix(imp, moduleName+"/") {
				edges = append(edges, from+" --> "+nodeID(imp))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	if len(edges) == 0 {
		t.Fatal("found no in-module imports at all, so the walk is wrong")
	}
	return edges
}

func nodeID(importPath string) string {
	if importPath == moduleName || importPath == moduleName+"/." {
		return "main"
	}
	return path.Base(importPath)
}

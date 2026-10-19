package project

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/protocol"
)

var ErrInvalidResourceContract = errors.New("invalid selected Resource contract")

// selectedResourceContracts binds access contract identities only to files in
// the verified Go-selected source closure. The compiler checks their Go types.
func selectedResourceContracts(packages []SourcePackage, projectModules []string) ([]analyzer.ResourceContract, error) {
	contracts := make([]analyzer.ResourceContract, 0)
	seenIDs := make(map[string]string)
	seenTypes := make(map[string]string)
	projects := make(map[string]struct{}, len(projectModules))
	for _, module := range projectModules {
		projects[module] = struct{}{}
	}
	for _, pkg := range packages {
		if _, project := projects[pkg.Module.Path]; !project {
			continue
		}
		for _, file := range pkg.Files {
			parsed, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Content, parser.ParseComments)
			if err != nil {
				if !bytes.Contains(file.Content, []byte("//plystra:resource")) {
					continue
				}
				return nil, fmt.Errorf("%w: parse selected source %s", ErrInvalidResourceContract, file.Path)
			}
			all := make(map[*ast.Comment]struct{})
			used := make(map[*ast.Comment]struct{})
			for _, group := range parsed.Comments {
				for _, comment := range group.List {
					if resourceDirective(comment.Text) {
						all[comment] = struct{}{}
					}
				}
			}
			for _, decl := range parsed.Decls {
				group, ok := decl.(*ast.GenDecl)
				if !ok || group.Tok != token.TYPE {
					continue
				}
				groupComments := resourceComments(group.Doc)
				if len(groupComments) != 0 && len(group.Specs) != 1 {
					return nil, fmt.Errorf("%w: ambiguous grouped type declaration in %s", ErrInvalidResourceContract, file.Path)
				}
				for _, item := range group.Specs {
					spec := item.(*ast.TypeSpec)
					comments := append(append([]*ast.Comment(nil), groupComments...), resourceComments(spec.Doc)...)
					if len(comments) == 0 {
						continue
					}
					if len(comments) != 1 || hasInterfaceDirective(group.Doc) || hasInterfaceDirective(spec.Doc) || spec.Name.Name != "Resource" || spec.Assign.IsValid() || spec.TypeParams != nil {
						return nil, fmt.Errorf("%w: invalid directive in %s", ErrInvalidResourceContract, file.Path)
					}
					if _, ok := spec.Type.(*ast.InterfaceType); !ok {
						return nil, fmt.Errorf("%w: directive requires an interface in %s", ErrInvalidResourceContract, file.Path)
					}
					id, ok := parseResourceDirective(comments[0].Text)
					if !ok {
						return nil, fmt.Errorf("%w: malformed directive in %s", ErrInvalidResourceContract, file.Path)
					}
					key := pkg.ImportPath + "\x00" + spec.Name.Name
					location := pkg.ImportPath + ":" + file.Path
					if previous, exists := seenTypes[key]; exists {
						return nil, fmt.Errorf("%w: duplicate type at %s and %s", ErrInvalidResourceContract, previous, location)
					}
					if previous, exists := seenIDs[id]; exists {
						return nil, fmt.Errorf("%w: duplicate ID %s at %s and %s", ErrInvalidResourceContract, id, previous, location)
					}
					seenTypes[key] = location
					seenIDs[id] = location
					used[comments[0]] = struct{}{}
					contracts = append(contracts, analyzer.ResourceContract{ImportPath: pkg.ImportPath, TypeName: "Resource", ID: id})
					if len(contracts) > protocol.MaxSnapshotFiles {
						return nil, fmt.Errorf("%w: too many contracts", ErrInvalidResourceContract)
					}
				}
			}
			if len(all) != len(used) {
				return nil, fmt.Errorf("%w: unbound directive in %s", ErrInvalidResourceContract, file.Path)
			}
		}
	}
	sort.Slice(contracts, func(i, j int) bool { return contracts[i].ID < contracts[j].ID })
	return contracts, nil
}

func resourceComments(group *ast.CommentGroup) []*ast.Comment {
	if group == nil {
		return nil
	}
	var comments []*ast.Comment
	for _, comment := range group.List {
		if resourceDirective(comment.Text) {
			comments = append(comments, comment)
		}
	}
	return comments
}

func resourceDirective(text string) bool {
	return strings.HasPrefix(text, "//plystra:resource")
}

func hasInterfaceDirective(group *ast.CommentGroup) bool {
	if group == nil {
		return false
	}
	for _, comment := range group.List {
		if strings.HasPrefix(comment.Text, "//plystra:interface") {
			return true
		}
	}
	return false
}

func parseResourceDirective(text string) (string, bool) {
	fields := strings.Fields(strings.TrimPrefix(text, "//"))
	if len(fields) != 2 || fields[0] != "plystra:resource" || !declaration.ValidMemberID(fields[1]) {
		return "", false
	}
	return fields[1], true
}

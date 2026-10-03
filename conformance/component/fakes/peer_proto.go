package fakes

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// CompileProtos compiles every .proto under contracts, resolving imports from contracts, then
// from each of imports (be-protocol proto/), then from the standard well-known types.
func CompileProtos(contracts fs.FS, imports ...fs.FS) (map[string]protoreflect.MethodDescriptor, error) {
	var files []string
	err := fs.WalkDir(contracts, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".proto") {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	roots := append([]fs.FS{contracts}, imports...)
	c := protocompile.Compiler{Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
		Accessor: func(name string) (io.ReadCloser, error) {
			for _, r := range roots {
				if f, err := r.Open(name); err == nil {
					return f, nil
				}
			}
			return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
		},
	})}
	compiled, err := c.Compile(context.Background(), files...)
	if err != nil {
		return nil, err
	}
	methods := map[string]protoreflect.MethodDescriptor{}
	for _, f := range compiled {
		svcs := f.Services()
		for i := 0; i < svcs.Len(); i++ {
			ms := svcs.Get(i).Methods()
			for j := 0; j < ms.Len(); j++ {
				m := ms.Get(j)
				methods[string(svcs.Get(i).FullName())+"/"+string(m.Name())] = m
			}
		}
	}
	return methods, nil
}

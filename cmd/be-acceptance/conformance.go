package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	compconf "github.com/brickKit/be-acceptance/conformance/component"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// parseConformanceArgs parses `conformance component …`.
func parseConformanceArgs(args []string) (compconf.Options, error) {
	var o compconf.Options
	if len(args) < 1 || args[0] != "component" {
		return o, fmt.Errorf("用法：be-acceptance conformance component --dir <组件根目录> --image <镜像> [--out <目录>] [--profiles core,obs,…] [--dep-contracts <依赖ID>=<proto 目录>] [--keep]")
	}
	fs := flag.NewFlagSet("conformance component", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.Dir, "dir", "", "被测组件根目录（component.yaml、assembly.yaml、contracts/、conformance/）")
	fs.StringVar(&o.Image, "image", "", "被测镜像")
	fs.StringVar(&o.OutDir, "out", ".", "compconf-report.json / .md 写到这里")
	fs.StringVar(&o.Prefix, "prefix", "", "一次性容器名前缀（默认 sdkb-acc-<随机>）")
	fs.StringVar(&o.FakeHost, "fake-host", "", "容器访问套件假服务用的主机名（默认 host.docker.internal）")
	fs.BoolVar(&o.Keep, "keep", false, "跑完保留容器与运行目录，便于排查")
	profiles := fs.String("profiles", "", "只跑这些 profile（逗号分隔）")
	var deps multiFlag
	fs.Var(&deps, "dep-contracts", "依赖的 proto 目录：<依赖ID>=<目录>，可重复")
	if err := fs.Parse(args[1:]); err != nil {
		return o, err
	}
	if o.Dir == "" || o.Image == "" {
		return o, fmt.Errorf("--dir 与 --image 都必须给")
	}
	if *profiles != "" {
		for _, p := range strings.Split(*profiles, ",") {
			if !isProfile(p) {
				return o, fmt.Errorf("profile %q 未知", p)
			}
			o.Profiles = append(o.Profiles, p)
		}
	}
	o.DepContracts = map[string]string{}
	for _, d := range deps {
		id, dir, ok := strings.Cut(d, "=")
		if !ok {
			return o, fmt.Errorf("--dep-contracts %q 应为 <依赖ID>=<目录>", d)
		}
		o.DepContracts[id] = dir
	}
	return o, nil
}

func isProfile(p string) bool {
	cat, err := compconf.LoadCatalog()
	if err != nil {
		return false
	}
	for _, n := range cat.ProfileNames() {
		if n == p {
			return true
		}
	}
	return false
}

// runConformance runs the component conformance suite; a failed MUST case exits 1.
func runConformance(args []string) error {
	o, err := parseConformanceArgs(args)
	if err != nil {
		return usageError{err}
	}
	o.Progress = os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := compconf.Execute(ctx, o)
	if err != nil {
		return err
	}
	fmt.Printf("compconf %s %s：%s（报告：%s/compconf-report.json）\n", rep.Component, rep.Version, rep.Result, o.OutDir)
	if rep.Result != "pass" {
		return fmt.Errorf("组件一致性套件判红")
	}
	return nil
}

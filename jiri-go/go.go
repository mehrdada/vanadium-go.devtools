// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The following enables go generate to generate the doc.go file.
//go:generate go run $V23_ROOT/release/go/src/v.io/x/lib/cmdline/testdata/gendoc.go . -help

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
	"time"

	"v.io/jiri/lib/collect"
	"v.io/jiri/lib/project"
	"v.io/jiri/lib/runutil"
	"v.io/jiri/lib/tool"
	"v.io/jiri/lib/util"
	"v.io/x/devtools/internal/buildinfo"
	"v.io/x/lib/cmdline"
	"v.io/x/lib/envvar"
	"v.io/x/lib/metadata"
	"v.io/x/lib/set"
)

func init() {
	tool.InitializeRunFlags(&cmdGo.Flags)
}

// cmdGo represents the "v23 go" command.
var cmdGo = &cmdline.Command{
	Runner: cmdline.RunnerFunc(runGo),
	Name:   "go",
	Short:  "Execute the go tool using the vanadium environment",
	Long: `
Wrapper around the 'go' tool that can be used for compilation of
vanadium Go sources. It takes care of vanadium-specific setup, such as
setting up the Go specific environment variables or making sure that
VDL generated files are regenerated before compilation.

In particular, the tool invokes the following command before invoking
any go tool commands that compile vanadium Go code:

vdl generate -lang=go all
`,
	ArgsName: "<arg ...>",
	ArgsLong: "<arg ...> is a list of arguments for the go tool.",
}

func runGo(cmdlineEnv *cmdline.Env, args []string) error {
	if len(args) == 0 {
		return cmdlineEnv.UsageErrorf("not enough arguments")
	}
	ctx := tool.NewContextFromEnv(cmdlineEnv)

	env, err := util.VanadiumEnvironment(ctx)
	if err != nil {
		return err
	}

	switch args[0] {
	case "build", "install":
		// Provide default ldflags to populate build info metadata in the
		// binary. Any manual specification of ldflags already in the args
		// will override this.
		var err error
		args, err = setBuildInfoFlags(ctx, args, env)
		if err != nil {
			return err
		}
		fallthrough
	case "generate", "run", "test":
		// Check that all non-master branches have been merged with the
		// master branch to make sure the vdl tool is not run against
		// out-of-date code base.
		if err := reportOutdatedBranches(ctx); err != nil {
			return err
		}

		// Generate vdl files, if necessary.
		if err := generateVDL(ctx, env, args); err != nil {
			return err
		}
	}

	// Run the go tool.
	goBin, err := runutil.LookPath("go", env.ToMap())
	if err != nil {
		return err
	}
	opts := ctx.Run().Opts()
	opts.Env = env.ToMap()
	return util.TranslateExitCode(ctx.Run().CommandWithOpts(opts, goBin, args...))
}

// getPlatform identifies the target platform by querying the go tool
// for the values of the GOARCH and GOOS environment variables.
func getPlatform(ctx *tool.Context, env *envvar.Vars) (string, error) {
	goBin, err := runutil.LookPath("go", env.ToMap())
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stdout = &out
	opts.Env = env.ToMap()
	if err = ctx.Run().CommandWithOpts(opts, goBin, "env", "GOARCH"); err != nil {
		return "", err
	}
	arch := strings.TrimSpace(out.String())
	out.Reset()
	if err = ctx.Run().CommandWithOpts(opts, goBin, "env", "GOOS"); err != nil {
		return "", err
	}
	os := strings.TrimSpace(out.String())
	return fmt.Sprintf("%s-%s", arch, os), nil
}

// setBuildInfoFlags augments the list of arguments with flags for the
// go compiler that encoded the build information expected by the
// v.io/x/lib/metadata package.
func setBuildInfoFlags(ctx *tool.Context, args []string, env *envvar.Vars) ([]string, error) {
	info := buildinfo.T{Time: time.Now()}
	// Compute the "platform" value.
	platform, err := getPlatform(ctx, env)
	if err != nil {
		return nil, err
	}
	info.Platform = platform
	// Compute the "manifest" value.
	manifest, err := project.CurrentManifest(ctx)
	if err != nil {
		return nil, err
	}
	info.Manifest = *manifest
	// Compute the "pristine" value.
	states, err := project.GetProjectStates(ctx, true)
	if err != nil {
		return nil, err
	}
	info.Pristine = true
	for _, state := range states {
		if state.CurrentBranch != "master" || state.HasUncommitted || state.HasUntracked {
			info.Pristine = false
			break
		}
	}
	// Compute the "user" value.
	if currUser, err := user.Current(); err == nil {
		info.User = currUser.Name
	}
	// Encode buildinfo as metadata and extract the appropriate ldflags.
	md, err := info.ToMetaData()
	if err != nil {
		return nil, err
	}
	ldflags := "-ldflags=" + metadata.LDFlag(md)
	return append([]string{args[0], ldflags}, args[1:]...), nil
}

// generateVDL generates VDL for the transitive Go package
// dependencies.
//
// Note that the vdl tool takes VDL packages as input, but we're
// supplying Go packages.  We're assuming the package paths for the
// VDL packages we want to generate have the same path names as the Go
// package paths.  Some of the Go package paths may not correspond to
// a valid VDL package, so we provide the -ignore_unknown flag to
// silently ignore these paths.
//
// It's fine if the VDL packages have dependencies not reflected in
// the Go packages; the vdl tool will compute the transitive closure
// of VDL package dependencies, as usual.
//
// TODO(toddw): Change the vdl tool to return vdl packages given the
// full Go dependencies, after vdl config files are implemented.
func generateVDL(ctx *tool.Context, env *envvar.Vars, cmdArgs []string) error {
	// Compute which VDL-based Go packages might need to be regenerated.
	goPkgs, goFiles, goTags := processGoCmdAndArgs(cmdArgs[0], cmdArgs[1:])
	goDeps, err := computeGoDeps(ctx, env, append(goPkgs, goFiles...), goTags)
	if err != nil {
		return err
	}

	// Regenerate the VDL-based Go packages.
	vdlArgs := []string{"-ignore_unknown", "generate", "-lang=go"}
	vdlArgs = append(vdlArgs, goDeps...)
	vdlBin, err := exec.LookPath("vdl")
	if err != nil {
		return err
	}
	var out bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stdout = &out
	opts.Stderr = &out
	opts.Env = env.ToMap()
	if err := ctx.Run().CommandWithOpts(opts, vdlBin, vdlArgs...); err != nil {
		return fmt.Errorf("failed to generate vdl: %v\n%s", err, out.String())
	}
	return nil
}

// reportOutdatedProjects checks if the currently checked out branches
// are up-to-date with respect to the local master branch. For each
// branch that is not, a notification is printed.
func reportOutdatedBranches(ctx *tool.Context) (e error) {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	defer collect.Error(func() error { return ctx.Run().Chdir(cwd) }, &e)
	projects, err := project.LocalProjects(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		if err := ctx.Run().Chdir(project.Path); err != nil {
			return err
		}
		switch project.Protocol {
		case "git":
			branches, _, err := ctx.Git().GetBranches("--merged")
			if err != nil {
				return err
			}
			found := false
			for _, branch := range branches {
				if branch == "master" {
					found = true
					break
				}
			}
			merging, err := ctx.Git().MergeInProgress()
			if err != nil {
				return err
			}
			if !found && !merging {
				fmt.Fprintf(ctx.Stderr(), "NOTE: project=%q path=%q\n", project.Name, project.Path)
				fmt.Fprintf(ctx.Stderr(), "This project is on a non-master branch that is out of date.\n")
				fmt.Fprintf(ctx.Stderr(), "Please update this branch using %q.\n", "git merge master")
				fmt.Fprintf(ctx.Stderr(), "Until then the %q tool might not function properly.\n", "v23")
			}
		}
	}
	return nil
}

// processGoCmdAndArgs is given the cmd and args for the go tool, filters out
// flags, and returns the PACKAGES or GOFILES that were specified in args, as
// well as "foo" if -tags=foo was specified in the args.  Note that all commands
// that accept PACKAGES also accept GOFILES.
//
//   go build    [build flags]              [-o out]      [PACKAGES]
//   go generate                            [-run regexp] [PACKAGES]
//   go install  [build flags]                            [PACKAGES]
//   go run      [build flags]              [-exec prog]  [GOFILES]  [run args]
//   go test     [build flags] [test flags] [-exec prog]  [PACKAGES] [testbin flags]
//
// Sadly there's no way to do this syntactically.  It's easy for single token
// -flag and -flag=x, but non-boolean flags may be two tokens "-flag x".
//
// We keep track of all non-boolean flags F, and skip every token that starts
// with - or --, and also skip the next token if the flag is in F and isn't of
// the form -flag=x.  If we forget to update F, we'll still handle the -flag and
// -flag=x cases correctly, but we'll get "-flag x" wrong.
func processGoCmdAndArgs(cmd string, args []string) ([]string, []string, string) {
	var goTags string
	var nonBool map[string]bool
	switch cmd {
	case "build":
		nonBool = nonBoolGoBuild
	case "generate":
		nonBool = nonBoolGoGenerate
	case "install":
		nonBool = nonBoolGoInstall
	case "run":
		nonBool = nonBoolGoRun
	case "test":
		nonBool = nonBoolGoTest
	}

	// Move start to the start of PACKAGES or GOFILES, by skipping flags.
	start := 0
	for start < len(args) {
		// Handle special-case terminator --
		if args[start] == "--" {
			start++
			break
		}
		match := goFlagRE.FindStringSubmatch(args[start])
		if match == nil {
			break
		}
		// Skip this flag, and maybe skip the next token for the "-flag x" case.
		//   match[1] is the flag name
		//   match[2] is the optional "=" for the -flag=x case
		start++
		if nonBool[match[1]] && match[2] == "" {
			start++
		}
		// Grab the value of -tags, if it is specified.
		if match[1] == "tags" {
			if match[2] == "=" {
				goTags = match[3]
			} else {
				goTags = args[start-1]
			}
		}
	}

	// Move end to the end of PACKAGES or GOFILES.
	var end int
	switch cmd {
	case "test":
		// Any arg starting with - is a testbin flag.
		// https://golang.org/cmd/go/#hdr-Test_packages
		for end = start; end < len(args); end++ {
			if strings.HasPrefix(args[end], "-") {
				break
			}
		}
	case "run":
		// Go run takes gofiles, which are defined as a file ending in ".go".
		// https://golang.org/cmd/go/#hdr-Compile_and_run_Go_program
		for end = start; end < len(args); end++ {
			if !strings.HasSuffix(args[end], ".go") {
				break
			}
		}
	default:
		end = len(args)
	}

	// Decide whether these are packages or files.
	switch {
	case start == end:
		return nil, nil, goTags
	case (start < len(args) && strings.HasSuffix(args[start], ".go")):
		return nil, args[start:end], goTags
	default:
		return args[start:end], nil, goTags
	}
}

var (
	goFlagRE     = regexp.MustCompile(`^--?([^=]+)(=?)(.*)`)
	nonBoolBuild = []string{
		"p", "ccflags", "compiler", "gccgoflags", "gcflags", "installsuffix", "ldflags", "tags",
	}
	nonBoolTest = []string{
		"bench", "benchtime", "blockprofile", "blockprofilerate", "covermode", "coverpkg", "coverprofile", "cpu", "cpuprofile", "memprofile", "memprofilerate", "outputdir", "parallel", "run", "timeout",
	}
	nonBoolGoBuild    = set.StringBool.FromSlice(append(nonBoolBuild, "o"))
	nonBoolGoGenerate = set.StringBool.FromSlice([]string{"run"})
	nonBoolGoInstall  = set.StringBool.FromSlice(nonBoolBuild)
	nonBoolGoRun      = set.StringBool.FromSlice(append(nonBoolBuild, "exec"))
	nonBoolGoTest     = set.StringBool.FromSlice(append(append(nonBoolBuild, nonBoolTest...), "exec"))
)

// computeGoDeps computes the transitive Go package dependencies for the given
// set of pkgs.  The strategy is to run "go list <pkgs>" with a special format
// string that dumps the specified pkgs and all deps as space / newline
// separated tokens.  The pkgs may be in any format recognized by "go list"; dir
// paths, import paths, or go files.
func computeGoDeps(ctx *tool.Context, env *envvar.Vars, pkgs []string, goTags string) ([]string, error) {
	goBin, err := runutil.LookPath("go", env.ToMap())
	if err != nil {
		return nil, err
	}
	goListArgs := []string{`list`, `-f`, `{{.ImportPath}} {{join .Deps " "}}`}
	if goTags != "" {
		goListArgs = append(goListArgs, "-tags="+goTags)
	}
	goListArgs = append(goListArgs, pkgs...)
	var stdout, stderr bytes.Buffer
	// TODO(jsimsa): Avoid buffering all of the output in memory
	// either by extending the runutil API to support piping of
	// output, or by writing the output to a temporary file
	// instead of an in-memory buffer.
	opts := ctx.Run().Opts()
	opts.Stdout = &stdout
	opts.Stderr = &stderr
	opts.Env = env.ToMap()
	if err := ctx.Run().CommandWithOpts(opts, goBin, goListArgs...); err != nil {
		return nil, fmt.Errorf("failed to compute go deps: %v\n%s\n%v", err, stderr.String(), pkgs)
	}
	scanner := bufio.NewScanner(&stdout)
	scanner.Split(bufio.ScanWords)
	depsMap := make(map[string]bool)
	for scanner.Scan() {
		depsMap[scanner.Text()] = true
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("Scan() failed: %v", err)
	}
	var deps []string
	for dep, _ := range depsMap {
		// Filter out bad packages:
		//   command-line-arguments is the dummy import path for "go run".
		switch dep {
		case "command-line-arguments":
			continue
		}
		deps = append(deps, dep)
	}
	return deps, nil
}

func main() {
	cmdline.Main(cmdGo)
}

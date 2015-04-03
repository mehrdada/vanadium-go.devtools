// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testutil

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"v.io/x/devtools/internal/collect"
	"v.io/x/devtools/internal/envutil"
	"v.io/x/devtools/internal/goutil"
	"v.io/x/devtools/internal/tool"
	"v.io/x/devtools/internal/util"
	"v.io/x/devtools/internal/xunit"
	"v.io/x/lib/host"
)

type taskStatus int

const (
	buildPassed taskStatus = iota
	buildFailed
	testPassed
	testFailed
	testTimedout
)

type buildResult struct {
	pkg    string
	status taskStatus
	output string
	time   time.Duration
}

type goBuildOpt interface {
	goBuildOpt()
}

type goCoverageOpt interface {
	goCoverageOpt()
}

type goTestOpt interface {
	goTestOpt()
}

type funcMatcherOpt struct{ funcMatcher }

type nonTestArgsOpt []string
type argsOpt []string
type profilesOpt []string
type timeoutOpt string
type suffixOpt string
type excludedTestsOpt []test
type pkgsOpt []string

func (argsOpt) goBuildOpt()    {}
func (argsOpt) goCoverageOpt() {}
func (argsOpt) goTestOpt()     {}

func (nonTestArgsOpt) goTestOpt() {}

func (profilesOpt) goBuildOpt()    {}
func (profilesOpt) goCoverageOpt() {}
func (profilesOpt) goTestOpt()     {}

func (timeoutOpt) goCoverageOpt() {}
func (timeoutOpt) goTestOpt()     {}

func (suffixOpt) goTestOpt() {}

func (excludedTestsOpt) goTestOpt() {}

func (funcMatcherOpt) goTestOpt() {}

func (pkgsOpt) goTestOpt()     {}
func (pkgsOpt) goBuildOpt()    {}
func (pkgsOpt) goCoverageOpt() {}

// goBuild is a helper function for running Go builds.
func goBuild(ctx *tool.Context, testName string, opts ...goBuildOpt) (_ *TestResult, e error) {
	args, profiles, pkgs := []string{}, []string{}, []string{}
	for _, opt := range opts {
		switch typedOpt := opt.(type) {
		case argsOpt:
			args = []string(typedOpt)
		case profilesOpt:
			profiles = []string(typedOpt)
		case pkgsOpt:
			pkgs = []string(typedOpt)
		}
	}

	// Initialize the test.
	cleanup, err := initTest(ctx, testName, profiles)
	if err != nil {
		return nil, internalTestError{err, "Init"}
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	// Enumerate the packages to be built.
	pkgList, err := goutil.List(ctx, pkgs)
	if err != nil {
		return nil, err
	}

	// Create a pool of workers.
	numPkgs := len(pkgList)
	tasks := make(chan string, numPkgs)
	taskResults := make(chan buildResult, numPkgs)
	for i := 0; i < runtime.NumCPU(); i++ {
		go buildWorker(ctx, args, tasks, taskResults)
	}

	// Distribute work to workers.
	for _, pkg := range pkgList {
		tasks <- pkg
	}
	close(tasks)

	// Collect the results.
	allPassed, suites := true, []xunit.TestSuite{}
	for i := 0; i < numPkgs; i++ {
		result := <-taskResults
		s := xunit.TestSuite{Name: result.pkg}
		c := xunit.TestCase{
			Classname: result.pkg,
			Name:      "Build",
			Time:      fmt.Sprintf("%.2f", result.time.Seconds()),
		}
		if result.status != buildPassed {
			Fail(ctx, "%s\n%v\n", result.pkg, result.output)
			f := xunit.Failure{
				Message: "build",
				Data:    result.output,
			}
			c.Failures = append(c.Failures, f)
			allPassed = false
			s.Failures++
		} else {
			Pass(ctx, "%s\n", result.pkg)
		}
		s.Tests++
		s.Cases = append(s.Cases, c)
		suites = append(suites, s)
	}
	close(taskResults)

	// Create the xUnit report.
	if err := xunit.CreateReport(ctx, testName, suites); err != nil {
		return nil, err
	}
	if !allPassed {
		return &TestResult{Status: TestFailed}, nil
	}
	return &TestResult{Status: TestPassed}, nil
}

// buildWorker builds packages.
func buildWorker(ctx *tool.Context, args []string, pkgs <-chan string, results chan<- buildResult) {
	opts := ctx.Run().Opts()
	opts.Verbose = false
	for pkg := range pkgs {
		var out bytes.Buffer
		args := append([]string{"go", "build", "-o", filepath.Join(binDirPath(), path.Base(pkg))}, args...)
		args = append(args, pkg)
		opts.Stdout = &out
		opts.Stderr = &out
		start := time.Now()
		err := ctx.Run().CommandWithOpts(opts, "v23", args...)
		duration := time.Now().Sub(start)
		result := buildResult{
			pkg:    pkg,
			time:   duration,
			output: out.String(),
		}
		if err != nil {
			result.status = buildFailed
		} else {
			result.status = buildPassed
		}
		results <- result
	}
}

type coverageResult struct {
	pkg      string
	coverage *os.File
	output   string
	status   taskStatus
	time     time.Duration
}

const defaultTestCoverageTimeout = "5m"

// goCoverage is a helper function for running Go coverage tests.
func goCoverage(ctx *tool.Context, testName string, opts ...goCoverageOpt) (_ *TestResult, e error) {
	timeout := defaultTestCoverageTimeout
	pkgs := []string{}
	args, profiles := []string{}, []string{}
	for _, opt := range opts {
		switch typedOpt := opt.(type) {
		case timeoutOpt:
			timeout = string(typedOpt)
		case argsOpt:
			args = []string(typedOpt)
		case profilesOpt:
			profiles = []string(typedOpt)
		case pkgsOpt:
			pkgs = []string(typedOpt)
		}
	}

	// Initialize the test.
	cleanup, err := initTest(ctx, testName, profiles)
	if err != nil {
		return nil, internalTestError{err, "Init"}
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	// Install dependencies.
	if err := installGoCover(ctx); err != nil {
		return nil, err
	}
	if err := installGoCoverCobertura(ctx); err != nil {
		return nil, err
	}
	if err := installGo2XUnit(ctx); err != nil {
		return nil, err
	}

	// Pre-build non-test packages.
	if err := buildTestDeps(ctx, pkgs); err != nil {
		if err := xunit.CreateFailureReport(ctx, testName, "BuildTestDependencies", "TestCoverage", "dependencies build failure", err.Error()); err != nil {
			return nil, err
		}
		return &TestResult{Status: TestFailed}, nil
	}

	// Enumerate the packages for which coverage is to be computed.
	pkgList, err := goutil.List(ctx, pkgs)
	if err != nil {
		return nil, err
	}

	// Create a pool of workers.
	numPkgs := len(pkgList)
	tasks := make(chan string, numPkgs)
	taskResults := make(chan coverageResult, numPkgs)
	for i := 0; i < runtime.NumCPU(); i++ {
		go coverageWorker(ctx, timeout, args, tasks, taskResults)
	}

	// Distribute work to workers.
	for _, pkg := range pkgList {
		tasks <- pkg
	}
	close(tasks)

	// Collect the results.
	//
	// TODO(jsimsa): Gather coverage data using the testCoverage
	// data structure as opposed to a buffer.
	var coverageData bytes.Buffer
	fmt.Fprintf(&coverageData, "mode: set\n")
	allPassed, suites := true, []xunit.TestSuite{}
	for i := 0; i < numPkgs; i++ {
		result := <-taskResults
		var s *xunit.TestSuite
		switch result.status {
		case buildFailed:
			s = xunit.CreateTestSuiteWithFailure(result.pkg, "TestCoverage", "build failure", result.output, result.time)
		case testPassed:
			data, err := ioutil.ReadAll(result.coverage)
			if err != nil {
				return nil, err
			}
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if line != "" && strings.Index(line, "mode: set") == -1 {
					fmt.Fprintf(&coverageData, "%s\n", line)
				}
			}
			fallthrough
		case testFailed:
			if strings.Index(result.output, "no test files") == -1 {
				ss, err := xunit.TestSuiteFromGoTestOutput(ctx, bytes.NewBufferString(result.output))
				if err != nil {
					// Token too long error.
					if !strings.HasSuffix(err.Error(), "token too long") {
						return nil, err
					}
					ss = xunit.CreateTestSuiteWithFailure(result.pkg, "Test", "test output contains lines that are too long to parse", "", result.time)
				}
				s = ss
			}
		}
		if result.coverage != nil {
			result.coverage.Close()
			if err := ctx.Run().RemoveAll(result.coverage.Name()); err != nil {
				return nil, err
			}
		}
		if s != nil {
			if s.Failures > 0 {
				allPassed = false
				Fail(ctx, "%s\n%v\n", result.pkg, result.output)
			} else {
				Pass(ctx, "%s\n", result.pkg)
			}
			suites = append(suites, *s)
		}
	}
	close(taskResults)

	// Create the xUnit and cobertura reports.
	if err := xunit.CreateReport(ctx, testName, suites); err != nil {
		return nil, err
	}
	coverage, err := coverageFromGoTestOutput(ctx, &coverageData)
	if err != nil {
		return nil, err
	}
	if err := createCoberturaReport(ctx, testName, coverage); err != nil {
		return nil, err
	}
	if !allPassed {
		return &TestResult{Status: TestFailed}, nil
	}
	return &TestResult{Status: TestPassed}, nil
}

// coverageWorker generates test coverage.
func coverageWorker(ctx *tool.Context, timeout string, args []string, pkgs <-chan string, results chan<- coverageResult) {
	opts := ctx.Run().Opts()
	opts.Verbose = false
	for pkg := range pkgs {
		// Compute the test coverage.
		var out bytes.Buffer
		coverageFile, err := ioutil.TempFile("", "")
		if err != nil {
			panic(fmt.Sprintf("TempFile() failed: %v", err))
		}
		args := append([]string{
			"go", "test", "-cover", "-coverprofile",
			coverageFile.Name(), "-timeout", timeout, "-v",
		}, args...)
		args = append(args, pkg)
		opts.Stdout = &out
		opts.Stderr = &out
		start := time.Now()
		err = ctx.Run().CommandWithOpts(opts, "v23", args...)
		result := coverageResult{
			pkg:      pkg,
			coverage: coverageFile,
			time:     time.Now().Sub(start),
			output:   out.String(),
		}
		if err != nil {
			if isBuildFailure(err, out.String(), pkg) {
				result.status = buildFailed
			} else {
				result.status = testFailed
			}
		} else {
			result.status = testPassed
		}
		results <- result
	}
}

// funcMatcher is the interface for determing if functions in the loaded ast
// of a package match a certain criteria.
type funcMatcher interface {
	match(*ast.FuncDecl) (bool, string)
}

type matchGoTestFunc struct{}

func (t *matchGoTestFunc) match(fn *ast.FuncDecl) (bool, string) {
	name := fn.Name.String()
	// TODO(cnicolaou): match on signature, not just name.
	return strings.HasPrefix(name, "Test"), name
}
func (t *matchGoTestFunc) goTestOpt() {}

type matchV23TestFunc struct{}

func (t *matchV23TestFunc) match(fn *ast.FuncDecl) (bool, string) {
	name := fn.Name.String()
	// TODO(cnicolaou): match on signature, not just name.
	return strings.HasPrefix(name, "TestV23"), name
}

func (t *matchV23TestFunc) goTestOpt() {}

// goListPackagesAndFuncs is a helper function for listing Go
// packages and obtaining lists of function names that are matched
// by the matcher interface.
func goListPackagesAndFuncs(ctx *tool.Context, pkgs []string, matcher funcMatcher) ([]string, map[string][]string, error) {

	env, err := util.VanadiumEnvironment(ctx, util.HostPlatform())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to obtain the Vanadium environment: %v", err)
	}
	pkgList, err := goutil.List(ctx, pkgs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list packages: %v", err)
	}

	matched := map[string][]string{}
	pkgsWithTests := []string{}

	buildContext := build.Default
	buildContext.GOPATH = env.Get("GOPATH")
	for _, pkg := range pkgList {
		pi, err := buildContext.Import(pkg, ".", build.ImportMode(0))
		if err != nil {
			return nil, nil, err
		}
		testFiles := append(pi.TestGoFiles, pi.XTestGoFiles...)
		fset := token.NewFileSet() // positions are relative to fset
		for _, testFile := range testFiles {
			file := filepath.Join(pi.Dir, testFile)
			testAST, err := parser.ParseFile(fset, file, nil, parser.Mode(0))
			if err != nil {
				return nil, nil, fmt.Errorf("failed parsing: %v: %v", file, err)
			}
			for _, decl := range testAST.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if ok, result := matcher.match(fn); ok {
					matched[pkg] = append(matched[pkg], result)
				}
			}
		}
		if len(matched[pkg]) > 0 {
			pkgsWithTests = append(pkgsWithTests, pkg)
		}
	}
	return pkgsWithTests, matched, nil
}

// filterExcludedTests filters out excluded tests returning an
// indication of whether this package should be included in test runs
// and a list of the specific tests that should be run (which if nil
// means running all of the tests), and a list of the skipped tests.
func filterExcludedTests(pkg string, testNames []string, excludedTests []test) (bool, []string, []string) {
	excluded := []string{}
	for _, name := range testNames {
		for _, exclude := range excludedTests {
			if exclude.pkgRE.MatchString(pkg) && exclude.nameRE.MatchString(name) {
				excluded = append(excluded, name)
				break
			}
		}
	}
	if len(excluded) == 0 {
		// Run all of the tests, none are to be skipped/excluded.
		return true, nil, nil
	}

	remaining := []string{}
	for _, name := range testNames {
		found := false
		for _, exclude := range excluded {
			if name == exclude {
				found = true
				break
			}
		}
		if !found {
			remaining = append(remaining, name)
		}
	}
	return len(remaining) > 0, remaining, excluded
}

type testResult struct {
	pkg      string
	output   string
	excluded []string
	status   taskStatus
	time     time.Duration
}

const defaultTestTimeout = "5m"

type goTestTask struct {
	pkg string
	// specificTests enumerates the tests to run:
	// if non-nil, pass to -run as a regex or'ing each item in the slice.
	// if nil, invoke the test without -run.
	specificTests []string
	// excludedTests enumerates the tests that are to be excluded as a result
	// of exclusion rules.
	excludedTests []string
}

// goTest is a helper function for running Go tests.
func goTest(ctx *tool.Context, testName string, opts ...goTestOpt) (_ *TestResult, e error) {
	timeout := defaultTestTimeout
	args, profiles, suffix, excludedTestRules, pkgs := []string{}, []string{}, "", []test{}, []string{}
	var matcher funcMatcher
	matcher = &matchGoTestFunc{}
	var nonTestArgs nonTestArgsOpt
	for _, opt := range opts {
		switch typedOpt := opt.(type) {
		case timeoutOpt:
			timeout = string(typedOpt)
		case argsOpt:
			args = []string(typedOpt)
		case profilesOpt:
			profiles = []string(typedOpt)
		case suffixOpt:
			suffix = string(typedOpt)
		case excludedTestsOpt:
			excludedTestRules = []test(typedOpt)
		case nonTestArgsOpt:
			nonTestArgs = typedOpt
		case funcMatcherOpt:
			matcher = typedOpt
		case pkgsOpt:
			pkgs = []string(typedOpt)
		}
	}

	// Initialize the test.
	cleanup, err := initTest(ctx, testName, profiles)
	if err != nil {
		return nil, internalTestError{err, "Init"}
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	ctx.Run().Opts().Env["V23_BIN_DIR"] = binDirPath()

	// Install dependencies.
	if err := installGo2XUnit(ctx); err != nil {
		return nil, err
	}

	// Pre-build non-test packages.
	if err := buildTestDeps(ctx, pkgs); err != nil {
		originalTestName := testName
		if len(suffix) != 0 {
			testName += " " + suffix
		}
		if err := xunit.CreateFailureReport(ctx, originalTestName, "BuildTestDependencies", testName, "dependencies build failure", err.Error()); err != nil {
			return nil, err
		}
		return &TestResult{Status: TestFailed}, nil
	}

	// Enumerate the packages and tests to be built.
	pkgList, pkgAndFuncList, err := goListPackagesAndFuncs(ctx, pkgs, matcher)
	if err != nil {
		return nil, err
	}

	// Create a pool of workers.
	numPkgs := len(pkgList)
	tasks := make(chan goTestTask, numPkgs)
	taskResults := make(chan testResult, numPkgs)
	for i := 0; i < runtime.NumCPU(); i++ {
		go testWorker(ctx, timeout, args, nonTestArgs, tasks, taskResults)
	}

	// Distribute work to workers.
	for _, pkg := range pkgList {
		testThisPkg, specificTests, excludedTests := filterExcludedTests(pkg, pkgAndFuncList[pkg], excludedTestRules)
		if testThisPkg {
			tasks <- goTestTask{pkg, specificTests, excludedTests}
		} else {
			taskResults <- testResult{
				pkg:      pkg,
				output:   "package excluded",
				excluded: excludedTests,
				status:   testPassed,
			}
		}
	}
	close(tasks)

	// Collect the results.

	// excludedTests are a result of exclusion rules in this tool.
	excludedTests := map[string][]string{}
	// skippedTests are a result of testing.Skip calls in the actual
	// tests.
	skippedTests := map[string][]string{}
	allPassed, suites := true, []xunit.TestSuite{}
	for i := 0; i < numPkgs; i++ {
		result := <-taskResults
		var s *xunit.TestSuite
		switch result.status {
		case buildFailed:
			s = xunit.CreateTestSuiteWithFailure(result.pkg, "Test", "build failure", result.output, result.time)
		case testFailed, testPassed:
			if strings.Index(result.output, "no test files") == -1 &&
				strings.Index(result.output, "package excluded") == -1 {
				ss, err := xunit.TestSuiteFromGoTestOutput(ctx, bytes.NewBufferString(result.output))
				if err != nil {
					// Token too long error.
					if !strings.HasSuffix(err.Error(), "token too long") {
						return nil, err
					}
					ss = xunit.CreateTestSuiteWithFailure(result.pkg, "Test", "test output contains lines that are too long to parse", "", result.time)
				}
				if ss.Skip > 0 {
					for _, c := range ss.Cases {
						if c.Skipped != nil {
							skippedTests[result.pkg] = append(skippedTests[result.pkg], c.Name)
						}
					}
				}
				s = ss
			}
			if len(result.excluded) > 0 {
				excludedTests[result.pkg] = result.excluded
			}
		}
		if s != nil {
			if s.Failures > 0 {
				allPassed = false
				Fail(ctx, "%s\n%v\n", result.pkg, result.output)
			} else {
				Pass(ctx, "%s\n", result.pkg)
			}
			if s.Skip > 0 {
				Pass(ctx, "%s (skipped tests: %v)\n", result.pkg, skippedTests[result.pkg])
			}

			newCases := []xunit.TestCase{}
			for _, c := range s.Cases {
				if len(suffix) != 0 {
					c.Name += " " + suffix
				}
				newCases = append(newCases, c)
			}
			s.Cases = newCases
			suites = append(suites, *s)
		}
		if excluded := excludedTests[result.pkg]; excluded != nil {
			Pass(ctx, "%s (excluded tests: %v)\n", result.pkg, excluded)
		}
	}
	close(taskResults)

	// Create the xUnit report.
	if err := xunit.CreateReport(ctx, testName, suites); err != nil {
		return nil, err
	}
	testResult := &TestResult{
		Status:        TestPassed,
		ExcludedTests: excludedTests,
		SkippedTests:  skippedTests,
	}
	if !allPassed {
		testResult.Status = TestFailed
	}
	return testResult, nil
}

// testWorker tests packages.
func testWorker(ctx *tool.Context, timeout string, args, nonTestArgs []string, tasks <-chan goTestTask, results chan<- testResult) {
	opts := ctx.Run().Opts()
	opts.Verbose = false
	for task := range tasks {
		// Run the test.
		taskArgs := append([]string{"go", "test", "-timeout", timeout, "-v"}, args...)
		if len(task.specificTests) != 0 {
			taskArgs = append(taskArgs, "-run", fmt.Sprintf("%s", strings.Join(task.specificTests, "|")))
		}
		taskArgs = append(taskArgs, task.pkg)
		taskArgs = append(taskArgs, nonTestArgs...)
		var out bytes.Buffer
		opts.Stdout = &out
		opts.Stderr = &out
		start := time.Now()
		err := ctx.Run().CommandWithOpts(opts, "v23", taskArgs...)
		result := testResult{
			pkg:      task.pkg,
			time:     time.Now().Sub(start),
			output:   out.String(),
			excluded: task.excludedTests,
		}
		if err != nil {
			if isBuildFailure(err, out.String(), task.pkg) {
				result.status = buildFailed
			} else {
				result.status = testFailed
			}
		} else {
			result.status = testPassed
		}
		results <- result
	}
}

// buildTestDeps builds dependencies for the given test packages
func buildTestDeps(ctx *tool.Context, pkgs []string) error {
	fmt.Fprintf(ctx.Stdout(), "building test dependencies ... ")
	args := append([]string{"go", "test", "-i"}, pkgs...)
	var out bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stderr = &out
	err := ctx.Run().CommandWithOpts(opts, "v23", args...)
	if err == nil {
		fmt.Fprintf(ctx.Stdout(), "ok\n")
		return nil
	}
	fmt.Fprintf(ctx.Stdout(), "failed\n%s\n", out.String())
	return fmt.Errorf("%v\n%s", err, out.String())
}

// installGoCover makes sure the "go cover" tool is installed.
//
// TODO(jsimsa): Unify the installation functions by moving the
// gocover-cobertura and go2xunit tools into the third_party
// project.
func installGoCover(ctx *tool.Context) error {
	// Check if the tool exists.
	var out bytes.Buffer
	cmd := exec.Command("go", "tool")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		if scanner.Text() == "cover" {
			return nil
		}
	}
	if scanner.Err() != nil {
		return fmt.Errorf("Scan() failed: %v")
	}
	if err := ctx.Run().Command("v23", "go", "install", "golang.org/x/tools/cmd/cover"); err != nil {
		return err
	}
	return nil
}

// installGoDoc makes sure the "go doc" tool is installed.
func installGoDoc(ctx *tool.Context) error {
	// Check if the tool exists.
	if _, err := exec.LookPath("godoc"); err != nil {
		if err := ctx.Run().Command("v23", "go", "install", "golang.org/x/tools/cmd/godoc"); err != nil {
			return err
		}
	}
	return nil
}

// installGoCoverCobertura makes sure the "gocover-cobertura" tool is
// installed.
func installGoCoverCobertura(ctx *tool.Context) error {
	root, err := util.VanadiumRoot()
	if err != nil {
		return err
	}
	// Check if the tool exists.
	bin, err := util.ThirdPartyBinPath(root, "gocover-cobertura")
	if err != nil {
		return err
	}
	if _, err := os.Stat(bin); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		opts := ctx.Run().Opts()
		if err := ctx.Run().CommandWithOpts(opts, "v23", "go", "install", "github.com/t-yuki/gocover-cobertura"); err != nil {
			return err
		}
	}
	return nil
}

// installGo2XUnit makes sure the "go2xunit" tool is installed.
func installGo2XUnit(ctx *tool.Context) error {
	root, err := util.VanadiumRoot()
	if err != nil {
		return err
	}
	// Check if the tool exists.
	bin, err := util.ThirdPartyBinPath(root, "go2xunit")
	if err != nil {
		return err
	}
	if _, err := os.Stat(bin); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		opts := ctx.Run().Opts()
		if err := ctx.Run().CommandWithOpts(opts, "v23", "go", "install", "bitbucket.org/tebeka/go2xunit"); err != nil {
			return err
		}
	}
	return nil
}

// isBuildFailure checks whether the given error and output indicate a build failure for the given package.
func isBuildFailure(err error, out, pkg string) bool {
	if exitError, ok := err.(*exec.ExitError); ok {
		// Try checking err's process state to determine the exit code.
		// Exit code 2 means build failures.
		if status, ok := exitError.Sys().(syscall.WaitStatus); ok {
			exitCode := status.ExitStatus()
			// A exit code of 2 means build failure.
			if exitCode == 2 {
				return true
			}
			// When the exit code is 1, we need to check the output to distinguish
			// "setup failure" and "test failure".
			if exitCode == 1 {
				// Treat setup failure as build failure.
				if strings.HasPrefix(out, fmt.Sprintf("# %s", pkg)) &&
					strings.HasSuffix(out, "[setup failed]\n") {
					return true
				}
				return false
			}
		}
	}
	// As a fallback, check the output line.
	// If the output starts with "# ${pkg}", then it should be a build failure.
	return strings.HasPrefix(out, fmt.Sprintf("# %s", pkg))
}

// getListenerPID finds the process ID of the process listening on the
// given port. If no process is listening on the given port (or an
// error is encountered), the function returns -1.
func getListenerPID(ctx *tool.Context, port string) (int, error) {
	// Make sure "lsof" exists.
	_, err := exec.LookPath("lsof")
	if err != nil {
		return -1, fmt.Errorf(`"lsof" not found in the PATH`)
	}

	// Use "lsof" to find the process ID of the listener.
	var out bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stdout = &out
	opts.Stderr = &out
	if err := ctx.Run().CommandWithOpts(opts, "lsof", "-i", ":"+port, "-sTCP:LISTEN", "-F", "p"); err != nil {
		// When no listener exists, "lsof" exits with non-zero
		// status.
		return -1, nil
	}

	// Parse the port number.
	pidString := strings.TrimPrefix(strings.TrimSpace(out.String()), "p")
	pid, err := strconv.Atoi(pidString)
	if err != nil {
		return -1, fmt.Errorf("Atoi(%v) failed: %v", pidString, err)
	}

	return pid, nil
}

type test struct {
	pkg    string
	name   string
	pkgRE  *regexp.Regexp
	nameRE *regexp.Regexp
}

type exclusion struct {
	desc    test
	exclude bool
}

var (
	exclusions []exclusion
)

func init() {
	exclusions = []exclusion{
		// This test triggers a bug in go 1.4.1 garbage collector.
		//
		// https://github.com/veyron/release-issues/issues/1494
		exclusion{test{pkg: "v.io/x/ref/profiles/internal/rpc/stream/vc", name: "TestConcurrentFlows"}, isDarwin() && is386()},
		// The fsnotify package tests are flaky on darwin. This begs the
		// question of whether we should be relying on this library at
		// all.
		exclusion{test{pkg: "github.com/howeyc/fsnotify", name: ".*"}, isDarwin()},
		// These tests are not maintained very well and are broken on all
		// platforms.
		// TODO(spetrovic): Put these back in once the owners fixes them.
		exclusion{test{pkg: "golang.org/x/mobile", name: ".*"}, true},
		// The following test requires IPv6, which is not available on
		// some of our continuous integration instances.
		exclusion{test{pkg: "golang.org/x/net/icmp", name: "TestPingGoogle"}, isCI()},
		// Don't run this test on mac systems prior to Yosemite since it
		// can crash some machines.
		exclusion{test{pkg: "golang.org/x/net/ipv6", name: ".*"}, !isYosemite()},
		// The following test is way out of date and doesn't work any more.
		exclusion{test{pkg: "golang.org/x/tools", name: "TestCheck"}, true},
		// The following two tests use too much memory.
		exclusion{test{pkg: "golang.org/x/tools/go/loader", name: "TestStdlib"}, true},
		exclusion{test{pkg: "golang.org/x/tools/go/ssa", name: "TestStdlib"}, true},
		// The following test expects to see "FAIL: TestBar" which causes
		// go2xunit to fail.
		exclusion{test{pkg: "golang.org/x/tools/go/ssa/interp", name: "TestTestmainPackage"}, true},
		// More broken tests.
		exclusion{test{pkg: "golang.org/x/tools/go/types", name: "TestCheck"}, true},
		exclusion{test{pkg: "golang.org/x/tools/refactor/lexical", name: "TestStdlib"}, true},
		exclusion{test{pkg: "golang.org/x/tools/refactor/importgraph", name: "TestBuild"}, true},
		// The godoc test does some really stupid string matching where it doesn't want
		// cmd/gc to appear, but we have v.io/x/ref/cmd/gclogs.
		exclusion{test{pkg: "golang.org/x/tools/cmd/godoc", name: "TestWeb"}, true},
	}
}

// ExcludedTests returns the set of tests to be excluded from
// the Vanadium projects.
func ExcludedTests() ([]test, error) {
	return excludedTests(exclusions)
}

func excludedTests(exclusions []exclusion) ([]test, error) {
	excluded := make([]test, 0, len(exclusions))
	for _, e := range exclusions {
		if e.exclude {
			var err error
			if e.desc.pkgRE, err = regexp.Compile(e.desc.pkg); err != nil {
				return nil, err
			}
			if e.desc.nameRE, err = regexp.Compile(e.desc.name); err != nil {
				return nil, err
			}
			excluded = append(excluded, e.desc)
		}
	}
	return excluded, nil
}

// validateAgainstDefaultPackages makes sure that the packages requested
// via opts are amongst the defaults assuming that all of the defaults are
// specified in <pkg>/... form and returns one of each of the goBuildOpt,
// goCoverageOpt and goTestOpt options.
// If no packages are requested, the defaults are returned.
// TODO(cnicolaou): ideally there'd be one piece of code that understands
//   go package specifications that could be used here.
func validateAgainstDefaultPackages(ctx *tool.Context, opts []TestOpt, defaults []string) (pkgsOpt, error) {

	optPkgs := []string{}
	for _, opt := range opts {
		switch v := opt.(type) {
		case PkgsOpt:
			optPkgs = []string(v)
		}
	}

	if len(optPkgs) == 0 {
		defsOpt := pkgsOpt(defaults)
		return defsOpt, nil
	}

	defPkgs, err := goutil.List(ctx, defaults)
	if err != nil {
		return nil, err
	}

	pkgs, err := goutil.List(ctx, optPkgs)
	if err != nil {
		return nil, err
	}

	for _, p := range pkgs {
		found := false
		for _, d := range defPkgs {
			if p == d {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("requested packages %v is not one of %v", p, defaults)
		}
	}
	po := pkgsOpt(pkgs)
	return po, nil
}

// getShortTestsOnlyOptValue gets the value of ShortTestsOnlyOpt from the given
// TestOpt slice.
func getShortTestsOnlyOptValue(opts []TestOpt) bool {
	shortTestsOnly := false
	for _, opt := range opts {
		switch v := opt.(type) {
		case ShortOpt:
			shortTestsOnly = bool(v)
		}
	}
	return shortTestsOnly
}

// thirdPartyGoBuild runs Go build for third-party projects.
func thirdPartyGoBuild(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := thirdPartyPkgs()
	if err != nil {
		return nil, err
	}
	validatedPkgs, err := validateAgainstDefaultPackages(ctx, opts, pkgs)
	if err != nil {
		return nil, err
	}
	profiles := profilesOpt([]string{"syncbase"})
	return goBuild(ctx, testName, validatedPkgs, profiles)
}

// thirdPartyGoTest runs Go tests for the third-party projects.
func thirdPartyGoTest(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := thirdPartyPkgs()
	if err != nil {
		return nil, err
	}
	validatedPkgs, err := validateAgainstDefaultPackages(ctx, opts, pkgs)
	if err != nil {
		return nil, err
	}
	exclusions, err := ExcludedTests()
	if err != nil {
		return nil, err
	}
	profiles := profilesOpt([]string{"syncbase"})
	suffix := suffixOpt(genTestNameSuffix("GoTest"))
	return goTest(ctx, testName, suffix, excludedTestsOpt(exclusions), validatedPkgs, profiles)
}

// thirdPartyGoRace runs Go data-race tests for third-party projects.
func thirdPartyGoRace(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := thirdPartyPkgs()
	if err != nil {
		return nil, err
	}
	validatedPkgs, err := validateAgainstDefaultPackages(ctx, opts, pkgs)
	if err != nil {
		return nil, err
	}
	args := argsOpt([]string{"-race"})
	exclusions, err := ExcludedTests()
	if err != nil {
		return nil, err
	}
	profiles := profilesOpt([]string{"syncbase"})
	suffix := suffixOpt(genTestNameSuffix("GoRace"))
	return goTest(ctx, testName, suffix, args, excludedTestsOpt(exclusions), validatedPkgs, profiles)
}

// thirdPartyPkgs returns a list of Go expressions that describe all
// third-party packages.
func thirdPartyPkgs() ([]string, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}

	thirdPartyDir := filepath.Join(root, "third_party", "go", "src")
	fileInfos, err := ioutil.ReadDir(thirdPartyDir)
	if err != nil {
		return nil, fmt.Errorf("ReadDir(%v) failed: %v", thirdPartyDir, err)
	}

	pkgs := []string{}
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			pkgs = append(pkgs, fileInfo.Name()+"/...")
		}
	}
	return pkgs, nil
}

// vanadiumGoBench runs Go benchmarks for vanadium projects.
func vanadiumGoBench(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	args := argsOpt([]string{"-bench", ".", "-run", "XXX"})
	return goTest(ctx, testName, args, pkgs)
}

// vanadiumGoBuild runs Go build for the vanadium projects.
func vanadiumGoBuild(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	return goBuild(ctx, testName, pkgs)
}

// vanadiumGoCoverage runs Go coverage tests for vanadium projects.
func vanadiumGoCoverage(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {

	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	return goCoverage(ctx, testName, pkgs)
}

// vanadiumGoDoc (re)starts the godoc server for vanadium projects.
func vanadiumGoDoc(ctx *tool.Context, testName string, _ ...TestOpt) (_ *TestResult, e error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}

	// Initialize the test.
	cleanup, err := initTest(ctx, testName, nil)
	if err != nil {
		return nil, internalTestError{err, "Init"}
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	// Install dependencies.
	if err := installGoDoc(ctx); err != nil {
		return nil, err
	}

	// Terminate previous instance of godoc if it is still running.
	godocPort := "8002"
	pid, err := getListenerPID(ctx, godocPort)
	if err != nil {
		return nil, err
	}
	if pid != -1 {
		p, err := os.FindProcess(pid)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(ctx.Stdout(), "kill %d\n", pid)
		if err := p.Kill(); err != nil {
			return nil, err
		}
	}

	// Start a new instance of godoc.
	//
	// Jenkins kills all background processes started by a shell
	// when the shell exits. To prevent Jenkins from doing that,
	// use nil as standard input, redirect output to a file, and
	// set the BUILD_ID environment variable to "dontKillMe".
	assetsPath := filepath.Join(os.Getenv("HOME"), "godoc")
	godocCmd := exec.Command(
		"godoc",
		"-analysis=type",
		"-goroot="+assetsPath,
		"-http=127.0.0.1:"+godocPort,
		"-index",
		"-templates="+assetsPath,
	)
	godocCmd.Stdin = nil
	fd, err := os.Create(filepath.Join(root, "godoc.out"))
	if err != nil {
		return nil, err
	}
	godocCmd.Stdout = fd
	godocCmd.Stderr = fd
	env := envutil.NewSnapshotFromOS()
	env.Set("BUILD_ID", "dontKillMe")
	env.Set("GOPATH", fmt.Sprintf("%v:%v", filepath.Join(root, "release", "go"), filepath.Join(root, "roadmap", "go")))
	godocCmd.Env = env.Slice()
	fmt.Fprintf(ctx.Stdout(), "%v %v\n", godocCmd.Env, strings.Join(godocCmd.Args, " "))
	if err := godocCmd.Start(); err != nil {
		return nil, err
	}

	return &TestResult{Status: TestPassed}, nil
}

// vanadiumGoGenerate checks that files created by 'go generate' are
// up-to-date.
func vanadiumGoGenerate(ctx *tool.Context, testName string, opts ...TestOpt) (_ *TestResult, e error) {
	cleanup, err := initTest(ctx, testName, []string{})
	if err != nil {
		return nil, internalTestError{err, "Init"}
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	pkgStr := strings.Join([]string(pkgs), " ")
	fmt.Fprintf(ctx.Stdout(), "NOTE: This test checks that files created by 'go generate' are up-to-date.\nIf it fails, regenerate them using 'v23 go generate %s'.\n", pkgStr)

	// Stash any uncommitted changes and defer functions that undo any
	// changes created by this function and then unstash the original
	// uncommitted changes.
	projects, err := util.LocalProjects(ctx)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if err := ctx.Run().Chdir(project.Path); err != nil {
			return nil, err
		}
		stashed, err := ctx.Git().Stash()
		if err != nil {
			return nil, err
		}
		defer collect.Error(func() error {
			if err := ctx.Run().Chdir(project.Path); err != nil {
				return err
			}
			if err := ctx.Git().Reset("HEAD"); err != nil {
				return err
			}
			if stashed {
				return ctx.Git().StashPop()
			}
			return nil
		}, &e)
	}

	// Check if 'go generate' creates any changes.
	args := append([]string{"go", "generate"}, []string(pkgs)...)
	if err := ctx.Run().Command("v23", args...); err != nil {
		return nil, internalTestError{err, "Go Generate"}
	}
	dirtyFiles := []string{}
	for _, project := range projects {
		files, err := ctx.Git(tool.RootDirOpt(project.Path)).FilesWithUncommittedChanges()
		if err != nil {
			return nil, err
		}
		dirtyFiles = append(dirtyFiles, files...)
	}
	if len(dirtyFiles) != 0 {
		output := strings.Join(dirtyFiles, "\n")
		fmt.Fprintf(ctx.Stdout(), "The following go generated files are not up-to-date:\n%v\n", output)
		// Generate xUnit report.
		suites := []xunit.TestSuite{}
		for _, dirtyFile := range dirtyFiles {
			s := xunit.CreateTestSuiteWithFailure("GoGenerate", dirtyFile, "go generate failure", "Outdated file:\n"+dirtyFile, 0)
			suites = append(suites, *s)
		}
		if err := xunit.CreateReport(ctx, testName, suites); err != nil {
			return nil, err
		}
		return &TestResult{Status: TestFailed}, nil
	}
	return &TestResult{Status: TestPassed}, nil
}

// vanadiumGoRace runs Go data-race tests for vanadium projects.
func vanadiumGoRace(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	partPkgs, err := identifyPackagesToTest(ctx, testName, opts, pkgs)
	if err != nil {
		return nil, err
	}
	exclusions, err := ExcludedTests()
	if err != nil {
		return nil, err
	}
	args := argsOpt([]string{"-race"})
	if getShortTestsOnlyOptValue(opts) {
		args = append(args, "-short")
	}
	timeout := timeoutOpt("15m")
	suffix := suffixOpt(genTestNameSuffix("GoRace"))
	return goTest(ctx, testName, args, timeout, suffix, excludedTestsOpt(exclusions), partPkgs)
}

// identifyPackagesToTest returns a slice of packages to test using the
// following algorithm:
// - The part index is stored in the "P" environment variable. If it is not
//   defined, return all packages.
// - If the part index is found, return the corresponding packages read and
//   processed from the config file. Note that for a test T with N parts, we
//   only specify the packages for the first N-1 parts in the config file. The
//   last part will automatically include all the packages that are not found
//   in the first N-1 parts.
func identifyPackagesToTest(ctx *tool.Context, testName string, opts []TestOpt, allPkgs []string) (pkgsOpt, error) {
	// Read config file to get the part.
	config, err := util.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	parts := config.TestParts(testName)
	if len(parts) == 0 {
		return pkgsOpt(allPkgs), nil
	}

	// Get part index from optionals.
	index := 0
	for _, opt := range opts {
		switch v := opt.(type) {
		case PartOpt:
			index = int(v)
		}
	}

	if index == len(parts) {
		// Special handling for getting the packages other than the packages
		// specified in "test-parts".

		// Get packages specified in test-parts.
		existingPartsPkgs := map[string]struct{}{}
		for _, pkg := range parts {
			curPkgs, err := goutil.List(ctx, []string{pkg})
			if err != nil {
				return nil, err
			}
			for _, curPkg := range curPkgs {
				existingPartsPkgs[curPkg] = struct{}{}
			}
		}

		// Get the rest.
		rest := []string{}
		allPkgs, err := goutil.List(ctx, allPkgs)
		if err != nil {
			return nil, err
		}
		for _, pkg := range allPkgs {
			if _, ok := existingPartsPkgs[pkg]; !ok {
				rest = append(rest, pkg)
			}
		}
		return pkgsOpt(rest), nil
	} else if index < len(parts) {
		pkgs, err := goutil.List(ctx, []string{parts[index]})
		if err != nil {
			return nil, err
		}
		return pkgsOpt(pkgs), nil
	}
	return nil, fmt.Errorf("invalid part index: %d/%d", index, len(parts)-1)
}

func vanadiumIntegrationTest(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	suffix := suffixOpt(genTestNameSuffix("V23Test"))
	args := argsOpt([]string{"-run", "^TestV23"})
	nonTestArgs := nonTestArgsOpt([]string{"-v23.tests"})
	matcher := funcMatcherOpt{&matchV23TestFunc{}}
	result, err := goTest(ctx, testName, suffix, args, nonTestArgs, matcher, pkgs)
	return result, err
}

func genTestNameSuffix(baseSuffix string) string {
	suffixParts := []string{}
	suffixParts = append(suffixParts, runtime.GOOS)
	arch := os.Getenv("GOARCH")
	if arch == "" {
		var err error
		arch, err = host.Arch()
		if err != nil {
			arch = "amd64"
		}
	}
	suffixParts = append(suffixParts, arch)
	suffix := strings.Join(suffixParts, ",")

	if baseSuffix == "" {
		return fmt.Sprintf("[%s]", suffix)
	}
	return fmt.Sprintf("[%s - %s]", baseSuffix, suffix)
}

// vanadiumGoTest runs Go tests for vanadium projects.
func vanadiumGoTest(ctx *tool.Context, testName string, opts ...TestOpt) (*TestResult, error) {
	pkgs, err := validateAgainstDefaultPackages(ctx, opts, []string{"v.io/..."})
	if err != nil {
		return nil, err
	}
	exclusions, err := ExcludedTests()
	if err != nil {
		return nil, err
	}
	args := argsOpt([]string{})
	if getShortTestsOnlyOptValue(opts) {
		args = append(args, "-short")
	}
	suffix := suffixOpt(genTestNameSuffix("GoTest"))
	return goTest(ctx, testName, suffix, excludedTestsOpt(exclusions), pkgs, args)
}

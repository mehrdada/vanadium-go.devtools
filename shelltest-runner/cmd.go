package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"tools/lib/cmdline"
	"tools/lib/envutil"
	"tools/lib/runutil"
	"tools/lib/util"
	"tools/lib/version"
)

const (
	defaultBinDir   = "$TMPDIR/bin"
	outputPrefix    = "[SHELLTEST-RUNNER]"
	xunitReportFile = "tests_veyron_shell_test.xml"
)

var (
	// Flags.
	binDirFlag         string
	numTestWorkersFlag int
	verboseFlag        bool

	// Other vars.
	numCPU int
)

// All the GO binaries needed in shell test scripts.
//
// TODO(jingjin): move all the test scripts to GO programs, and make
// them talk to a build service to request binaries (with caching).
var binPackages = []string{
	"veyron.io/apps/tunnel/tunneld",
	"veyron.io/apps/tunnel/vsh",
	"veyron.io/playground/builder",
	"veyron.io/veyron/veyron/security/agent/agentd",
	"veyron.io/veyron/veyron/security/agent/pingpong",
	"veyron.io/veyron/veyron/services/mgmt/application/applicationd",
	"veyron.io/veyron/veyron/services/mgmt/binary/binaryd",
	"veyron.io/veyron/veyron/services/mgmt/build/buildd",
	"veyron.io/veyron/veyron/services/mgmt/profile/profiled",
	"veyron.io/veyron/veyron/services/mounttable/mounttabled",
	"veyron.io/veyron/veyron/services/proxy/proxyd",
	"veyron.io/veyron/veyron/tools/application",
	"veyron.io/veyron/veyron/tools/binary",
	"veyron.io/veyron/veyron/tools/build",
	"veyron.io/veyron/veyron/tools/debug",
	"veyron.io/veyron/veyron/tools/mounttable",
	"veyron.io/veyron/veyron/tools/principal",
	"veyron.io/veyron/veyron/tools/profile",
	"veyron.io/veyron/veyron/tools/naming/simulator",
	"veyron.io/veyron/veyron2/vdl/vdl",
	"veyron.io/wspr/veyron/services/wsprd",
}

func init() {
	cmdRoot.Flags.StringVar(&binDirFlag, "bin_dir", defaultBinDir, "The binary directory.")
	cmdRoot.Flags.IntVar(&numTestWorkersFlag, "workers", 0, "Number of test workers. The default 0 matches the number of CPUs.")
	cmdRoot.Flags.BoolVar(&verboseFlag, "v", false, "Print verbose output.")

	// Set the number of OS threads that can run Go code
	// simultaneously to the number of cpu cores.
	numCPU = runtime.NumCPU()
	runtime.GOMAXPROCS(numCPU)
}

// substituteVarsInFlags substitutes environment variables in default
// values of relevant flags.
func substituteVarsInFlags() {
	if binDirFlag == defaultBinDir {
		binDirFlag = filepath.Join(os.TempDir(), "bin")
	}
}

// root returns a command that represents the root of the
// shelltest-runner tool.
func root() *cmdline.Command {
	return cmdRoot
}

// cmdRoot represents the root of the shelltest-runner tool.
var cmdRoot = &cmdline.Command{
	Name:     "shelltest-runner",
	Short:    "Tool for running shell test scripts",
	Long:     "Tool for running shell test scripts.",
	Run:      runRoot,
	Children: []*cmdline.Command{cmdVersion},
}

func runRoot(command *cmdline.Command, _ []string) error {
	ctx := util.NewContext(verboseFlag, command.Stdout(), command.Stderr())

	// Builds all GO binaries used in test scripts and then runs
	// the integration tests. This allows us to build these
	// binaries in parallel using goroutines to speed things up
	// and all tests can share these binaries.
	if err := buildBinaries(ctx); err != nil {
		return err
	}

	// Run test scripts.
	if err := runTestScripts(ctx); err != nil {
		return err
	}

	return nil
}

// buildBinaries builds GO binaries specified by binPackages list.
func buildBinaries(ctx *util.Context) error {
	// Prepare output dir for binaries.
	if err := ctx.Run().Function(runutil.RemoveAll(binDirFlag)); err != nil {
		return fmt.Errorf("RemoveAll(%q) failed: %v", binDirFlag, err)
	}
	if err := ctx.Run().Function(runutil.MkdirAll(binDirFlag, 0700)); err != nil {
		return fmt.Errorf("MkdirAll(%q) failed: %v", binDirFlag, err)
	}

	// Create a worker pool for building binaries in parallel.
	printf(ctx.Stdout(), "Building binaries...\n")
	numPkgs := len(binPackages)

	// We are going to send package name to the jobs channel.
	jobs := make(chan string, numPkgs)
	results := make(chan error, numPkgs)
	env := envutil.ToMap(os.Environ())
	for i := 0; i < numWorkers(numPkgs); i++ {
		go buildWorker(ctx, env, jobs, results)
	}

	// Send packages to free workers in the pool.
	for _, pkg := range binPackages {
		jobs <- pkg
	}
	close(jobs)

	// Gather results ignoring any build errors because they are
	// likely caused by outdated packages.
	for i := 0; i < numPkgs; i++ {
		<-results
	}
	close(results)
	return nil
}

// buildWorker waits for a package on the "jobs" channel and builds it.
func buildWorker(ctx *util.Context, env map[string]string, jobs <-chan string, results chan<- error) {
	for pkg := range jobs {
		output := path.Base(pkg)
		var stdout, stderr bytes.Buffer
		buildArgs := []string{"go", "build", "-o", filepath.Join(binDirFlag, output), pkg}
		cmd := exec.Command("veyron", buildArgs...)
		cmd.Stdout, cmd.Stderr, cmd.Env = &stdout, &stderr, envutil.ToSlice(env)
		if err := cmd.Run(); err != nil {
			printf(ctx.Stdout(), "FAIL: %s (%s)\n%v\n", output, pkg, stderr.String())
			results <- err
		} else {
			printf(ctx.Stdout(), "OK: %s (%s)\n", output, pkg)
			results <- nil
		}
	}
}

type testResult struct {
	testName string
	passed   bool
	err      error
	stdout   string
	stderr   string
	duration int
}
type testResults []testResult

// For sorting testResults.
func (r testResults) Len() int           { return len(r) }
func (r testResults) Swap(i, j int)      { r[i], r[j] = r[j], r[i] }
func (r testResults) Less(i, j int) bool { return r[i].testName < r[j].testName }

// runTestScripts runs all test.sh scripts found under given root dirs.
func runTestScripts(ctx *util.Context) error {
	veyronRoot, err := util.VeyronRoot()
	if err != nil {
		return err
	}

	// Find all test.sh scripts.
	testScripts := findTestScripts([]string{
		filepath.Join(veyronRoot, "veyron", "go", "src"),
		filepath.Join(veyronRoot, "roadmap", "go", "src"),
	})

	// Create a worker pool to run tests in parallel.
	printf(ctx.Stdout(), "Running tests...\n")
	numTests := len(testScripts)

	// We are going to send test script path to the jobs channel.
	jobs := make(chan string, numTests)
	results := make(chan testResult, numTests)
	env := envutil.NewSnapshotFromOS()

	// Pass binDirFlag to test scripts through shell_test_BIN_DIR.
	env.Set("shell_test_BIN_DIR", binDirFlag)
	numTestWorkers := numWorkers(numTests)
	if numTestWorkersFlag > 0 {
		numTestWorkers = numTestWorkersFlag
	}
	for i := 0; i < numTestWorkers; i++ {
		go testWorker(ctx, veyronRoot, env.Map(), jobs, results)
	}

	// Output unfinished tests when the program is terminated.
	unfinishedTests := map[string]struct{}{}
	for _, testScript := range testScripts {
		unfinishedTests[testName(veyronRoot, testScript)] = struct{}{}
	}
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := (<-sigchan).(syscall.Signal)
		tests := []string{}
		for test := range unfinishedTests {
			tests = append(tests, test)
		}
		sort.Strings(tests)
		fmt.Fprintf(ctx.Stdout(), "\n\n")
		printf(ctx.Stdout(), "Unfinished tests:\n")
		for _, test := range tests {
			printf(ctx.Stdout(), "%s\n", test)
		}
		os.Exit(128 + int(s))
	}()

	// Send test scripts to free workers in the pool.
	for _, testScript := range testScripts {
		jobs <- testScript
	}
	close(jobs)

	// Gather results and find failed tests.
	failedTests := testResults{}
	allTests := testResults{}
	for i := 0; i < numTests; i++ {
		result := <-results
		allTests = append(allTests, result)
		if !result.passed {
			failedTests = append(failedTests, result)
		}
		delete(unfinishedTests, result.testName)
	}
	sort.Sort(failedTests)
	sort.Sort(allTests)
	close(results)

	// Output details for failed tests.
	for _, failedTest := range failedTests {
		fmt.Fprintln(ctx.Stdout())
		printf(ctx.Stdout(), "Failed test: %s\n%v\n%s\n%s\n",
			failedTest.testName, failedTest.err, failedTest.stdout, failedTest.stderr)
	}

	// Output xunit xml file.
	output, err := outputXUnitReport(allTests, failedTests)
	if err != nil {
		return err
	}
	fileMode := os.FileMode(0644)
	if err := ioutil.WriteFile(xunitReportFile, []byte(output), fileMode); err != nil {
		return fmt.Errorf("WriteFile(%q, %q, %v) failed: %v", xunitReportFile, output, fileMode, err)
	}

	if len(failedTests) > 0 {
		return fmt.Errorf("some tests failed: %v", failedTests)
	}
	return nil
}

// testWorker waits for test script on "jobs" channel and run it.
func testWorker(ctx *util.Context, root string, env map[string]string, jobs <-chan string, results chan<- testResult) {
	for testScript := range jobs {
		name := testName(root, testScript)
		var stdout, stderr bytes.Buffer
		cmd := exec.Command(testScript)
		cmd.Stdout, cmd.Stderr, cmd.Env = &stdout, &stderr, envutil.ToSlice(env)
		startTime := time.Now()
		if err := cmd.Run(); err != nil {
			printf(ctx.Stdout(), "FAIL: %s\n", name)
			results <- testResult{
				testName: name,
				passed:   false,
				err:      err,
				stdout:   stdout.String(),
				stderr:   stderr.String(),
				duration: int(time.Now().Sub(startTime).Seconds()),
			}
		} else {
			printf(ctx.Stdout(), "PASS: %s\n", name)
			results <- testResult{
				testName: name,
				passed:   true,
				duration: int(time.Now().Sub(startTime).Seconds()),
			}
		}
	}
}

// findTestScripts finds all test.sh file from the given root dirs.
func findTestScripts(rootDirs []string) []string {
	matchedFiles := []string{}
	for _, rootDir := range rootDirs {
		filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if strings.HasSuffix(path, string(os.PathSeparator)+"test.sh") {
				matchedFiles = append(matchedFiles, path)
			}
			return nil
		})
	}
	return matchedFiles
}

// numWorkers gets number of workers in worker pool based on number of
// jobs and CPU cores.
func numWorkers(numJobs int) int {
	numWorkers := numCPU
	if numJobs < numWorkers {
		numWorkers = numJobs
	}
	return numWorkers
}

// outputXUnitReport outputs xunit xml report for the test results to
// the current directory.
func outputXUnitReport(allResults testResults, failedResults testResults) (string, error) {
	type failure struct {
		Type    string `xml:"type,attr"`
		Message string `xml:"message,attr"`
		Data    string `xml:",chardata"`
	}
	type testcase struct {
		Classname string   `xml:"classname,attr"`
		Name      string   `xml:"name,attr"`
		Time      string   `xml:"time,attr"`
		Failure   *failure `xml:"failure,omitempty"`
	}
	type testsuite struct {
		Name     string     `xml:"name,attr"`
		Tests    string     `xml:"tests,attr"`
		Errors   string     `xml:"errors,attr"`
		Failures string     `xml:"failures,attr"`
		Skip     string     `xml:"skip,attr"`
		Testcase []testcase `xml:"testcase"`
	}
	type testsuites struct {
		Testsuite []testsuite `xml:"testsuite"`
	}

	numFailedTests := len(failedResults)
	suites := testsuites{}
	suite := testsuite{
		Name:     "shell-test",
		Tests:    fmt.Sprintf("%d", len(allResults)),
		Errors:   fmt.Sprintf("%d", numFailedTests),
		Failures: fmt.Sprintf("%d", numFailedTests),
		Skip:     "0",
	}
	testCases := []testcase{}
	for _, result := range allResults {
		testCase := testcase{
			Classname: "shell-test",
			Name:      result.testName,
			Time:      fmt.Sprintf("%d", result.duration),
		}
		if !result.passed {
			// Use __{testName}__ as a place holder which
			// will be replaced by stdout/stderr wrapped
			// in CDATA later to prevent xml.Marshal will
			// encode line breaks/tabs etc.
			testCase.Failure = &failure{
				Data:    fmt.Sprintf("___%s___", result.testName),
				Type:    "bash.error",
				Message: "error",
			}
		}
		testCases = append(testCases, testCase)
	}
	suite.Testcase = testCases
	suites.Testsuite = []testsuite{suite}
	output, err := xml.MarshalIndent(suites, "", "  ")
	if err != nil {
		return "", fmt.Errorf("MarshalIndent(%#v) failed: %v", suites, err)
	}
	strOutput := fmt.Sprintf("%s%s", xml.Header, string(output))

	// Replace place holders with stdout+stderr wrapped in CDATA.
	for _, failedResult := range failedResults {
		strOutput = strings.Replace(
			strOutput,
			fmt.Sprintf("___%s___", failedResult.testName),
			fmt.Sprintf("\n<![CDATA[\n%s\n%s\n]]>\n", failedResult.stdout, failedResult.stderr), -1)
	}

	return strOutput, nil
}

// testName trims VEYRON_ROOT and test.sh from the given test script path.
func testName(root, testScript string) string {
	testName := strings.TrimPrefix(testScript, root+string(os.PathSeparator))
	return strings.TrimSuffix(testName, string(os.PathSeparator)+"test.sh")
}

// printf outputs the given message prefixed by outputPrefix.
func printf(out io.Writer, format string, args ...interface{}) {
	fmt.Fprintf(out, outputPrefix+" "+format, args...)
}

// cmdVersion represent the 'version' command of the shelltest-runner tool.
var cmdVersion = &cmdline.Command{
	Run:   runVersion,
	Name:  "version",
	Short: "Print version",
	Long:  "Print version of the shelltest-runner tool.",
}

func runVersion(command *cmdline.Command, _ []string) error {
	printf(command.Stdout(), "shelltest-runner tool version %v\n", version.Version)
	return nil
}
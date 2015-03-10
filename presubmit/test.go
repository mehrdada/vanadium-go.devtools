package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"v.io/x/devtools/lib/collect"
	"v.io/x/devtools/lib/testutil"
	"v.io/x/devtools/lib/util"
	"v.io/x/devtools/lib/xunit"
	"v.io/x/lib/cmdline"
)

var (
	reviewTargetRefsFlag string
	testFlag             string
)

func init() {
	cmdTest.Flags.StringVar(&projectsFlag, "projects", "", "The base names of the remote projects containing the CLs pointed by the refs, separated by ':'.")
	cmdTest.Flags.StringVar(&reviewTargetRefsFlag, "refs", "", "The review references separated by ':'.")
	cmdTest.Flags.StringVar(&manifestFlag, "manifest", "", "Name of the project manifest.")
	cmdTest.Flags.IntVar(&jenkinsBuildNumberFlag, "build_number", -1, "The number of the Jenkins build.")
	cmdTest.Flags.StringVar(&testFlag, "test", "", "The name of a single test to run.")
}

// cmdTest represents the 'test' command of the presubmit tool.
var cmdTest = &cmdline.Command{
	Name:  "test",
	Short: "Run tests for a CL",
	Long: `
This subcommand pulls the open CLs from Gerrit, runs tests specified in a config
file, and posts test results back to the corresponding Gerrit review thread.
`,
	Run: runTest,
}

const (
	mergeConflictTestClass    = "merge conflict"
	mergeConflictMessageTmpl  = "Possible merge conflict detected in %s.\nPresubmit tests will be executed after a new patchset that resolves the conflicts is submitted."
	nanoToMiliSeconds         = 1000000
	prepareTestBranchAttempts = 3
	unknownStatus             = "UNKNOWN"
)

type cl struct {
	clNumber int
	patchset int
	ref      string
	project  string
}

func (c cl) String() string {
	return fmt.Sprintf("http://go/vcl/%d/%d", c.clNumber, c.patchset)
}

// runTest implements the 'test' subcommand.
func runTest(command *cmdline.Command, args []string) (e error) {
	ctx := util.NewContextFromCommand(command, !noColorFlag, dryRunFlag, verboseFlag)

	// Basic sanity checks.
	if err := sanityChecks(command); err != nil {
		return err
	}

	// Record the current timestamp so we can get the correct postsubmit build
	// when processing the results.
	curTimestamp := time.Now().UnixNano() / nanoToMiliSeconds

	// Generate cls from the refs and projects flags.
	cls, err := parseCLs()
	if err != nil {
		return err
	}

	projects, tools, err := util.ReadManifest(ctx, manifestFlag)
	if err != nil {
		return err
	}

	// tmpBinDir is where developer tools are built after changes are
	// pulled from the target CLs.
	tmpBinDir := filepath.Join(vroot, "tmpBin")

	// Setup cleanup function for cleaning up presubmit test branch.
	cleanupFn := func() error {
		os.RemoveAll(tmpBinDir)
		return cleanupAllPresubmitTestBranches(ctx, projects)
	}
	defer collect.Error(func() error { return cleanupFn() }, &e)

	// Trap SIGTERM and SIGINT signal when the program is aborted
	// on Jenkins.
	go func() {
		sigchan := make(chan os.Signal, 1)
		signal.Notify(sigchan, syscall.SIGINT, syscall.SIGTERM)
		<-sigchan
		if err := cleanupFn(); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
		}
		// Linux convention is to use 128+signal as the exit
		// code. We use 0 here to let Jenkins properly mark a
		// run as "Aborted" instead of "Failed".
		os.Exit(0)
	}()

	// Prepare presubmit test branch.
	for i := 1; i <= prepareTestBranchAttempts; i++ {
		if failedCL, err := preparePresubmitTestBranch(ctx, cls, projects); err != nil {
			if i > 1 {
				fmt.Fprintf(ctx.Stdout(), "Attempt #%d:\n", i)
			}
			if failedCL != nil {
				fmt.Fprintf(ctx.Stderr(), "%s: %v\n", failedCL.String(), err)
			}
			if strings.Contains(err.Error(), "unable to access") {
				// Cannot access googlesource.com, try again.
				continue
			}
			if strings.Contains(err.Error(), "git pull") {
				// Possible merge conflict.
				if err := recordMergeConflict(ctx, failedCL); err != nil {
					return err
				}
				return nil
			}
			return err
		}
		break
	}

	// Rebuild developer tools and override VANADIUM_ROOT/bin.
	env, errs := rebuildDeveloperTools(ctx, projects, tools, tmpBinDir)
	if len(errs) > 0 {
		// Don't fail on errors.
		for _, err := range errs {
			printf(ctx.Stderr(), "%v\n", err)
		}
	}

	// Run the tests.
	printf(ctx.Stdout(), "### Running the presubmit test\n")
	prefix := fmt.Sprintf("presubmit/%d/%s", jenkinsBuildNumberFlag, os.Getenv("L"))
	opts := []testutil.TestOpt{testutil.ShortOpt(true), testutil.PrefixOpt(prefix)}
	if results, err := testutil.RunTests(ctx, env, []string{testFlag}, opts...); err == nil {
		result, ok := results[testFlag]
		if !ok {
			return fmt.Errorf("No test result found for %q", testFlag)
		}
		return writeTestStatusFile(ctx, *result, curTimestamp)
	} else {
		return err
	}
}

// sanityChecks performs basic sanity checks for various flags.
func sanityChecks(command *cmdline.Command) error {
	manifestFilePath, err := util.RemoteManifestFile(manifestFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(manifestFilePath); err != nil {
		return fmt.Errorf("Stat(%q) failed: %v", manifestFilePath, err)
	}
	if projectsFlag == "" {
		return command.UsageErrorf("-projects flag is required")
	}
	if reviewTargetRefsFlag == "" {
		return command.UsageErrorf("-refs flag is required")
	}
	return nil
}

// parseCLs parses cl info from refs and projects flag, and returns a
// slice of "cl" objects.
func parseCLs() ([]cl, error) {
	refs := strings.Split(reviewTargetRefsFlag, ":")
	projects := strings.Split(projectsFlag, ":")
	if got, want := len(refs), len(projects); got != want {
		return nil, fmt.Errorf("Mismatching lengths of %v and %v: %v vs. %v", refs, projects, len(refs), len(projects))
	}
	cls := []cl{}
	for i, ref := range refs {
		project := projects[i]
		clNumber, patchset, err := parseRefString(ref)
		if err != nil {
			return nil, err
		}
		cls = append(cls, cl{
			clNumber: clNumber,
			patchset: patchset,
			ref:      ref,
			project:  project,
		})
	}
	return cls, nil
}

// presubmitTestBranchName returns the name of the branch where the cl
// content is pulled.
func presubmitTestBranchName(ref string) string {
	return "presubmit_" + ref
}

// preparePresubmitTestBranch creates and checks out the presubmit
// test branch and pulls the CL there.
func preparePresubmitTestBranch(ctx *util.Context, cls []cl, projects map[string]util.Project) (_ *cl, e error) {
	strCLs := []string{}
	for _, cl := range cls {
		strCLs = append(strCLs, cl.String())
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("Getwd() failed: %v", err)
	}
	defer collect.Error(func() error { return ctx.Run().Chdir(wd) }, &e)
	if err := cleanupAllPresubmitTestBranches(ctx, projects); err != nil {
		return nil, fmt.Errorf("%v\n", err)
	}
	// Pull changes for each cl.
	printf(ctx.Stdout(), "### Preparing to test %s\n", strings.Join(strCLs, ", "))
	prepareFn := func(curCL cl) error {
		localRepo, ok := projects[curCL.project]
		if !ok {
			return fmt.Errorf("project %q not found", curCL.project)
		}
		localRepoDir := localRepo.Path
		if err := ctx.Run().Chdir(localRepoDir); err != nil {
			return fmt.Errorf("Chdir(%v) failed: %v", localRepoDir, err)
		}
		branchName := presubmitTestBranchName(curCL.ref)
		if err := ctx.Git().CreateAndCheckoutBranch(branchName); err != nil {
			return err
		}
		if err := ctx.Git().Pull(util.VanadiumGitRepoHost()+localRepo.Name, curCL.ref); err != nil {
			return err
		}
		return nil
	}
	for _, cl := range cls {
		if err := prepareFn(cl); err != nil {
			testutil.Fail(ctx, "pull changes from %s\n", cl.String())
			return &cl, err
		}
		testutil.Pass(ctx, "pull changes from %s\n", cl.String())
	}
	return nil, nil
}

// recordMergeConflict records possible merge conflict in the test status file
// and xUnit report.
func recordMergeConflict(ctx *util.Context, failedCL *cl) error {
	message := fmt.Sprintf(mergeConflictMessageTmpl, failedCL.String())
	if err := xunit.CreateFailureReport(ctx, testFlag, testFlag, "MergeConflict", message, message); err != nil {
		return nil
	}
	result := testutil.TestResult{
		Status:          testutil.TestFailedMergeConflict,
		MergeConflictCL: failedCL.String(),
	}
	// We use math.MaxInt64 here so that the logic that tries to find the newest
	// build before the given timestamp terminates after the first iteration.
	if err := writeTestStatusFile(ctx, result, math.MaxInt64); err != nil {
		return err
	}
	return nil
}

// rebuildDeveloperTools rebuilds developer tools (e.g. v23, vdl..) in a
// temporary directory, which is used to replace VANADIUM_ROOT/bin in the PATH.
func rebuildDeveloperTools(ctx *util.Context, projects util.Projects, tools util.Tools, tmpBinDir string) (map[string]string, []error) {
	errs := []error{}
	toolsProject, ok := projects["release.go.tools"]
	env := map[string]string{}
	if !ok {
		errs = append(errs, fmt.Errorf("tools project not found, not rebuilding tools."))
	} else {
		// Find target Tools.
		targetTools := []util.Tool{}
		for name, tool := range tools {
			if name == "v23" || name == "vdl" || name == "go-depcop" {
				targetTools = append(targetTools, tool)
			}
		}
		// Rebuild.
		for _, tool := range targetTools {
			if err := util.BuildTool(ctx, tmpBinDir, tool.Name, tool.Package, toolsProject); err != nil {
				errs = append(errs, err)
			}
		}
		// Create a new PATH that replaces VANADIUM_ROOT/bin
		// with the temporary directory in which the tools
		// were rebuilt.
		env["PATH"] = strings.Replace(os.Getenv("PATH"), filepath.Join(vroot, "bin"), tmpBinDir, -1)
	}
	return env, errs
}

// cleanupPresubmitTestBranch removes the presubmit test branch.
func cleanupAllPresubmitTestBranches(ctx *util.Context, projects util.Projects) (e error) {
	printf(ctx.Stdout(), "### Cleaning up\n")
	if err := util.CleanupProjects(ctx, projects, true); err != nil {
		return err
	}
	return nil
}

// writeTestStatusFile writes the given TestResult and timestamp to a JSON file.
// This file will be collected (along with the test report xUnit file) by the
// "master" presubmit project for generating final test results message.
//
// For more details, see comments in result.go.
func writeTestStatusFile(ctx *util.Context, result testutil.TestResult, curTimestamp int64) error {
	// Get the file path.
	workspace, fileName := os.Getenv("WORKSPACE"), fmt.Sprintf("status_%s.json", strings.Replace(testFlag, "-", "_", -1))
	statusFilePath := ""
	if workspace == "" {
		statusFilePath = filepath.Join(os.Getenv("HOME"), "tmp", testFlag, fileName)
	} else {
		statusFilePath = filepath.Join(workspace, fileName)
	}

	// Write to file.
	r := testResultInfo{
		Result:     result,
		TestName:   testFlag,
		SlaveLabel: os.Getenv("L"), // Slave label is stored in environment variable "L"
		Timestamp:  curTimestamp,
	}
	bytes, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("Marshal(%v) failed: %v", r, err)
	}
	if err := ctx.Run().WriteFile(statusFilePath, bytes, os.FileMode(0644)); err != nil {
		return fmt.Errorf("WriteFile(%v) failed: %v", statusFilePath, err)
	}
	return nil
}

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/template"

	"v.io/lib/cmdline"
	"v.io/tools/lib/collect"
	"v.io/tools/lib/gitutil"
	"v.io/tools/lib/testutil"
	"v.io/tools/lib/util"
)

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

const failureReportTmpl = `<?xml version="1.0" encoding="utf-8"?>
<testsuites>
  <testsuite name="timeout" tests="1" errors="0" failures="1" skip="0">
    <testcase classname="{{.ClassName}}" name="{{.TestName}}" time="0">
      <failure type="error">
<![CDATA[
{{.ErrorMessage}}
]]>
      </failure>
    </testcase>
  </testsuite>
</testsuites>
`

const (
	mergeConflictTestClass   = "merge conflict"
	mergeConflictMessageTmpl = "Possible merge conflict detected in %s.\nPresubmit tests will be executed after a new patchset that resolves the conflicts is submitted."
)

type cl struct {
	clNumber int
	patchset int
	ref      string
	repo     string
}

func (c cl) String() string {
	return fmt.Sprintf("http://go/vcl/%d/%d", c.clNumber, c.patchset)
}

type testResultInfo struct {
	result     testutil.TestResult
	testName   string
	slaveLabel string
}

// All the multi-configuration Jenkins projects.
var multiConfigurationProjects = map[string]struct{}{
	"vanadium-go-build":         struct{}{},
	"vanadium-go-test":          struct{}{},
	"vanadium-integration-test": struct{}{},
}

// isMultiConfigurationProject checks whether the given project is a
// multi-configuration project.
func isMultiConfigurationProject(projectName string) bool {
	_, ok := multiConfigurationProjects[projectName]
	return ok
}

// runTest implements the 'test' subcommand.
func runTest(command *cmdline.Command, args []string) (e error) {
	ctx := util.NewContextFromCommand(command, !noColorFlag, dryRunFlag, verboseFlag)

	// Basic sanity checks.
	manifestFilePath, err := util.RemoteManifestFile(manifestFlag)
	if err != nil {
		return err
	}
	if _, err := os.Stat(manifestFilePath); err != nil {
		return fmt.Errorf("Stat(%q) failed: %v", manifestFilePath, err)
	}
	if reposFlag == "" {
		return command.UsageErrorf("-repos flag is required")
	}
	if reviewTargetRefsFlag == "" {
		return command.UsageErrorf("-refs flag is required")
	}

	// Parse cls from refs and repos.
	cls, err := parseCLs()
	if err != nil {
		return err
	}

	// Parse the manifest file to get the local path for the repo.
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
	if failedCL, err := preparePresubmitTestBranch(ctx, cls, projects); err != nil {
		// When "git pull" fails, record merge conflict error in test status file
		// and xunit report.
		if failedCL != nil {
			fmt.Fprintf(ctx.Stderr(), "%s: %v\n", failedCL.String(), err)
		}
		if strings.Contains(err.Error(), "git pull") {
			message := fmt.Sprintf(mergeConflictMessageTmpl, failedCL.String())
			result := testutil.TestResult{
				Status:          testutil.TestFailedMergeConflict,
				MergeConflictCL: failedCL.String(),
			}
			if err := generateFailureReport(testFlag, mergeConflictTestClass, message); err != nil {
				return err
			}
			if err := writeTestStatusFile(ctx, map[string]*testutil.TestResult{testFlag: &result}); err != nil {
				return err
			}
			return nil
		}
		return err
	}

	// Rebuild the developer tool.
	toolsProject, ok := projects["release.go.tools"]
	env := map[string]string{}
	if !ok {
		printf(ctx.Stderr(), "tools project not found, not rebuilding tools.\n")
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
				printf(ctx.Stderr(), "%v\n", err)
			}
		}
		// Create a new PATH that replaces VANADIUM_ROOT/bin
		// with the temporary directory in which the tools
		// were rebuilt.
		env["PATH"] = strings.Replace(os.Getenv("PATH"), filepath.Join(vroot, "bin"), tmpBinDir, -1)
	}

	// Run the tests.
	printf(ctx.Stdout(), "### Running the tests\n")
	if results, err := testutil.RunTests(ctx, env, []string{testFlag}); err == nil {
		return writeTestStatusFile(ctx, results)
	} else {
		return err
	}
}

// parseCLs parses cl info from refs and repos flag, and returns a
// slice of "cl" objects.
func parseCLs() ([]cl, error) {
	refs := strings.Split(reviewTargetRefsFlag, ":")
	repos := strings.Split(reposFlag, ":")
	if got, want := len(refs), len(repos); got != want {
		return nil, fmt.Errorf("Mismatching lengths of %v and %v: %v vs. %v", refs, repos, len(refs), len(repos))
	}
	cls := []cl{}
	for i, ref := range refs {
		repo := repos[i]
		clNumber, patchset, err := parseRefString(ref)
		if err != nil {
			return nil, err
		}
		cls = append(cls, cl{
			clNumber: clNumber,
			patchset: patchset,
			ref:      ref,
			repo:     repo,
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
		localRepo, ok := projects[curCL.repo]
		if !ok {
			return fmt.Errorf("repo %q not found", curCL.repo)
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

// cleanupPresubmitTestBranch removes the presubmit test branch.
func cleanupAllPresubmitTestBranches(ctx *util.Context, projects map[string]util.Project) (e error) {
	printf(ctx.Stdout(), "### Cleaning up\n")
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Getwd() failed: %v", err)
	}
	defer collect.Error(func() error { return ctx.Run().Chdir(wd) }, &e)
	for _, project := range projects {
		localRepoDir := project.Path
		if err := ctx.Run().Chdir(localRepoDir); err != nil {
			return fmt.Errorf("Chdir(%v) failed: %v", localRepoDir, err)
		}
		if err := resetRepo(ctx); err != nil {
			return err
		}
	}
	return nil
}

// resetRepo cleans up untracked files and uncommitted changes of the
// current branch, checks out the master branch, and deletes all the
// other branches.
func resetRepo(ctx *util.Context) error {
	// Clean up changes and check out master.
	curBranchName, err := ctx.Git().CurrentBranchName()
	if err != nil {
		return err
	}
	if curBranchName != "master" {
		if err := ctx.Git().CheckoutBranch("master", gitutil.Force); err != nil {
			return err
		}
	}
	if err := ctx.Git().RemoveUntrackedFiles(); err != nil {
		return err
	}
	// Discard any uncommitted changes.
	if err := ctx.Git().Reset("HEAD"); err != nil {
		return err
	}

	// Delete all the other branches.
	// At this point we should be at the master branch.
	branches, _, err := ctx.Git().GetBranches()
	if err != nil {
		return err
	}
	for _, branch := range branches {
		if branch == "master" {
			continue
		}
		if strings.HasPrefix(branch, "presubmit_refs") {
			if err := ctx.Git().DeleteBranch(branch, gitutil.Force); err != nil {
				return nil
			}
		}
	}

	return nil
}

// lastCompletedBuildStatus gets the status of the last completed
// build for a given jenkins test.
func lastCompletedBuildStatus(testName string, slaveLabel string) (_ string, e error) {
	// Construct REST API URL to get build status.
	statusUrl, err := url.Parse(jenkinsHostFlag)
	if err != nil {
		return "", fmt.Errorf("Parse(%q) failed: %v", jenkinsHostFlag, err)
	}
	if isMultiConfigurationProject(testName) {
		statusUrl.Path = fmt.Sprintf("%s/job/%s/L=%s/lastCompletedBuild/api/json", statusUrl.Path, testName, slaveLabel)
	} else {
		statusUrl.Path = fmt.Sprintf("%s/job/%s/lastCompletedBuild/api/json", statusUrl.Path, testName)
	}
	statusUrl.RawQuery = url.Values{
		"token": {jenkinsTokenFlag},
	}.Encode()

	// Get and parse JSON response.
	var body io.Reader
	method, url, body := "GET", statusUrl.String(), nil
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return "", fmt.Errorf("NewRequest(%q, %q, %v) failed: %v", method, url, body, err)
	}
	req.Header.Add("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("Do(%v) failed: %v", req, err)
	}
	defer collect.Error(func() error { return res.Body.Close() }, &e)
	return parseLastCompletedBuildStatusJsonResponse(res.Body)
}

// parseLastCompletedBuildStatusJsonResponse parses whether the last
// completed build was successful or not.
func parseLastCompletedBuildStatusJsonResponse(reader io.Reader) (string, error) {
	r := bufio.NewReader(reader)
	var status struct {
		Result string
	}
	if err := json.NewDecoder(r).Decode(&status); err != nil {
		return "", fmt.Errorf("Decode() failed: %v", err)
	}

	return status.Result, nil
}

type testCase struct {
	ClassName string
	Name      string
	Status    string
}

func (t testCase) equal(t2 testCase) bool {
	return t.ClassName == t2.ClassName && t.Name == t2.Name
}

type failureType int

const (
	fixedFailure failureType = iota
	newFailure
	knownFailure
)

func (t failureType) String() string {
	switch t {
	case fixedFailure:
		return "FIXED FAILURE"
	case newFailure:
		return "NEW FAILURE"
	case knownFailure:
		return "KNOWN FAILURE"
	default:
		return "UNKNOWN FAILURE TYPE"
	}
}

// failedTestLinks maps from failure type to links.
type failedTestLinksMap map[failureType][]string

// failedTestCases gets a list of failed test cases from the most
// recent build of the given Jenkins test.
func failedTestCases(ctx *util.Context, testName string, slaveLabel string) (_ []testCase, e error) {
	jenkins := ctx.Jenkins(jenkinsHostFlag)
	getTestRerpotUri := fmt.Sprintf("job/%s/lastCompletedBuild/testReport/api/json", testName)
	if isMultiConfigurationProject(testName) {
		getTestRerpotUri = fmt.Sprintf("job/%s/L=%s/lastCompletedBuild/testReport/api/json", testName, slaveLabel)
	}
	getTestReportRes, err := jenkins.Invoke("GET", getTestRerpotUri, url.Values{
		"token": {jenkinsTokenFlag},
	})
	if err != nil {
		return []testCase{}, err
	}
	defer collect.Error(func() error { return getTestReportRes.Body.Close() }, &e)
	return parseFailedTestCases(getTestReportRes.Body)
}

// parseFailedTestCases parses testCases from the given test report
// JSON string.
func parseFailedTestCases(reader io.Reader) ([]testCase, error) {
	r := bufio.NewReader(reader)
	var testCases struct {
		Suites []struct {
			Cases []testCase
		}
	}
	failedTestCases := []testCase{}
	if err := json.NewDecoder(r).Decode(&testCases); err != nil {
		return failedTestCases, fmt.Errorf("Decode() failed: %v", err)
	}
	for _, suite := range testCases.Suites {
		for _, curCase := range suite.Cases {
			if curCase.Status == "FAILED" || curCase.Status == "REGRESSION" {
				failedTestCases = append(failedTestCases, curCase)
			}
		}
	}
	return failedTestCases, nil
}

// createFailedTestsReport returns links for failed tests grouped by
// failure types.
func createFailedTestsReport(ctx *util.Context, testResults []testResultInfo) (_ string, e error) {
	linksMap := failedTestLinksMap{}
	// seenTests maps the test full names to number of times they
	// have been seen in the test reports. This will be used to
	// properly generate links to failed tests.
	//
	// For example, if TestA is tested multiple times, then their
	// links will look like:
	//   http://.../TestA
	//   http://.../TestA_2
	//   http://.../TestA_3
	seenTests := map[string]int{}
	for _, testResult := range testResults {
		testName := testResult.testName
		// For a given test script this-is-a-test.sh, its test
		// report file is: tests_this_is_a_test.xml.
		junitReportFileName := fmt.Sprintf("tests_%s.xml", strings.Replace(testName, "-", "_", -1))
		// The collected junit test report is located at:
		// $WORKSPACE/test_results/$buildNumber/L=$slaveLabel,TEST=$testName/tests_xxx.xml
		//
		// See more details in result.go.
		junitReportFile := filepath.Join(
			os.Getenv("WORKSPACE"),
			"test_results",
			fmt.Sprintf("%d", jenkinsBuildNumberFlag),
			fmt.Sprintf("L=%s,TEST=%s", testResult.slaveLabel, testResult.testName),
			junitReportFileName)
		fdReport, err := os.Open(junitReportFile)
		if err != nil {
			printf(ctx.Stderr(), "Open(%q) failed: %v\n", junitReportFile, err)
			continue
		}
		defer collect.Error(func() error { return fdReport.Close() }, &e)
		curLinksMap, err := genFailedTestLinks(ctx, fdReport, seenTests, testName, testResult.slaveLabel, failedTestCases)
		if err != nil {
			printf(ctx.Stderr(), "%v\n", err)
			continue
		}
		for curFailureType, curLinks := range curLinksMap {
			linksMap[curFailureType] = append(linksMap[curFailureType], curLinks...)
		}
	}

	// Output links grouped by failure types.
	var buf bytes.Buffer
	for _, failureType := range []failureType{newFailure, knownFailure, fixedFailure} {
		curLinks, ok := linksMap[failureType]
		if !ok || len(curLinks) == 0 {
			continue
		}
		failureTypeStr := failureType.String()
		if len(curLinks) > 1 {
			failureTypeStr += "S"
		}
		fmt.Fprintf(&buf, "%s:\n%s\n\n", failureTypeStr, strings.Join(curLinks, "\n"))
	}
	return buf.String(), nil
}

func genFailedTestLinks(ctx *util.Context, reader io.Reader, seenTests map[string]int, testName string, slaveLabel string, getFailedTestCases func(*util.Context, string, string) ([]testCase, error)) (failedTestLinksMap, error) {
	// Get failed test cases from the corresponding Jenkins test to
	// compare with the failed tests from presubmit.
	failedTestCases, err := getFailedTestCases(ctx, testName, slaveLabel)
	if err != nil {
		printf(ctx.Stderr(), "%v\n", err)
	}

	// Parse xUnit report of the presubmit test.
	r := bufio.NewReader(reader)
	var testSuites struct {
		Testsuites []struct {
			Name      string `xml:"name,attr"`
			Failures  string `xml:"failures,attr"`
			Testcases []struct {
				Classname string `xml:"classname,attr"`
				Name      string `xml:"name,attr"`
				Failure   struct {
					Data string `xml:",chardata"`
				} `xml:"failure,omitempty"`
			} `xml:"testcase"`
		} `xml:"testsuite"`
	}
	if err := xml.NewDecoder(r).Decode(&testSuites); err != nil {
		return nil, fmt.Errorf("Decode() failed: %v", err)
	}

	linksMap := failedTestLinksMap{}
	curFailedTestCases := []testCase{}
	for _, curTestSuite := range testSuites.Testsuites {
		for _, curTestCase := range curTestSuite.Testcases {
			// Use test suite's name as the test case's class name if the
			// class name is empty.
			if curTestCase.Classname == "" {
				curTestCase.Classname = curTestSuite.Name
			}
			// Unescape test name and class name.
			curTestCase.Classname = html.UnescapeString(curTestCase.Classname)
			curTestCase.Name = html.UnescapeString(curTestCase.Name)

			testFullName := genTestFullName(curTestCase.Classname, curTestCase.Name)
			testKey := testFullName
			if slaveLabel != "" {
				testKey = fmt.Sprintf("%s-%s", testFullName, slaveLabel)
			}
			seenTests[testKey]++

			// A failed test.
			if curTestCase.Failure.Data != "" {
				link := genTestResultLink(curTestCase.Classname, curTestCase.Name, testFullName, seenTests[testKey], testName, slaveLabel)
				// Determine whether the curTestCase is a new failure or not.
				isNewFailure := true
				for _, prevFailedTestCase := range failedTestCases {
					if curTestCase.Classname == prevFailedTestCase.ClassName && curTestCase.Name == prevFailedTestCase.Name {
						isNewFailure = false
						break
					}
				}
				if isNewFailure {
					linksMap[newFailure] = append(linksMap[newFailure], link)
				} else {
					linksMap[knownFailure] = append(linksMap[knownFailure], link)
				}

				curFailedTestCases = append(curFailedTestCases, testCase{
					ClassName: curTestCase.Classname,
					Name:      curTestCase.Name,
				})
			}
		}
	}

	// Generate links for "fixed" tests and put them into linksMap[fixedFailure].
	for _, prevFailedTestCase := range failedTestCases {
		fixed := true
		for _, curFailedTestCase := range curFailedTestCases {
			if prevFailedTestCase.equal(curFailedTestCase) {
				fixed = false
				break
			}
		}
		if fixed {
			testFullName := genTestFullName(prevFailedTestCase.ClassName, prevFailedTestCase.Name)
			// To make things simpler we only show the names of the fixed tests.
			linksMap[fixedFailure] = append(linksMap[fixedFailure], fmt.Sprintf("- %s", testFullName))
		}
	}
	return linksMap, nil
}

func genTestFullName(className, testName string) string {
	testFullName := fmt.Sprintf("%s.%s", className, testName)
	// Replace the period "." in testFullName with
	// "::" to stop gmail from turning it into a
	// link automatically.
	return strings.Replace(testFullName, ".", "::", -1)
}

func genTestResultLink(className, testCaseName, testFullName string, suffix int, testName, slaveLabel string) string {
	packageName := "(root)"
	// In JUnit:
	// - If className contains ".", the part before the last "." becomes
	//   the package name, and the part after it becomes the class name.
	// - If className doesn't contain ".", the package name will be
	//   "(root)".
	if strings.Contains(className, ".") {
		lastDotIndex := strings.LastIndex(className, ".")
		packageName = className[0:lastDotIndex]
		className = className[lastDotIndex+1:]
	}
	safePackageName := safePackageOrClassName(packageName)
	safeClassName := safePackageOrClassName(className)
	safeTestCaseName := safeTestName(testCaseName)
	link := ""
	rawurl := fmt.Sprintf("http://goto.google.com/vpst/%d/L=%s,TEST=%s/testReport/%s/%s/%s",
		jenkinsBuildNumberFlag, slaveLabel, testName, safePackageName, safeClassName, safeTestCaseName)
	testResultUrl, err := url.Parse(rawurl)
	if err == nil {
		link = fmt.Sprintf("- %s\n%s", testFullName, testResultUrl.String())
		if suffix > 1 {
			link = fmt.Sprintf("%s_%d", link, suffix)
		}
	} else {
		link = fmt.Sprintf("- %s\n  Result link not available (%v)", testFullName, err)
	}
	return link
}

// safePackageOrClassName gets the safe name of the package or class
// name which will be used to construct the URL to a test case.
//
// The original implementation in junit jenkins plugin can be found
// here: http://git.io/iVD0yw
func safePackageOrClassName(name string) string {
	return reURLUnsafeChars.ReplaceAllString(name, "_")
}

// safeTestName gets the safe name of the test name which will be used
// to construct the URL to a test case. Note that this is different
// from getting the safe name for package or class.
//
// The original implementation in junit jenkins plugin can be found
// here: http://git.io/8X9o7Q
func safeTestName(name string) string {
	return reNotIdentifierChars.ReplaceAllString(name, "_")
}

// generateFailureReport generates a xunit test report file for
// the given failing test.
func generateFailureReport(testName string, className, errorMessage string) (e error) {
	type tmplData struct {
		ClassName    string
		TestName     string
		ErrorMessage string
	}
	tmpl, err := template.New("failureReport").Parse(failureReportTmpl)
	if err != nil {
		return fmt.Errorf("Parse(%q) failed: %v", failureReportTmpl, err)
	}
	reportFileName := fmt.Sprintf("tests_%s.xml", strings.Replace(testName, "-", "_", -1))
	reportFile := filepath.Join(vroot, "..", reportFileName)
	f, err := os.Create(reportFile)
	if err != nil {
		return fmt.Errorf("Create(%q) failed: %v", reportFile, err)
	}
	defer collect.Error(func() error { return f.Close() }, &e)
	return tmpl.Execute(f, tmplData{
		ClassName:    className,
		TestName:     testName,
		ErrorMessage: errorMessage,
	})
}

// postMessage posts the given message to Gerrit.
func postMessage(ctx *util.Context, message string, refs []string) error {
	// Basic sanity check for the Gerrit base URL.
	gerritHost, err := checkGerritBaseUrl()
	if err != nil {
		return err
	}

	// Parse .netrc file to get Gerrit credential.
	gerritCred, err := gerritHostCredential(gerritHost)
	if err != nil {
		return err
	}

	// Post the reviews.
	gerrit := ctx.Gerrit(gerritBaseUrlFlag, gerritCred.username, gerritCred.password)
	for _, ref := range refs {
		if err := gerrit.PostReview(ref, message, nil); err != nil {
			return err
		}
	}
	return nil
}

// writeTestStatusFile writes the given TestResult map to a JSON file.
// This file will be collected (along with the test report xunit file) by the
// "master" presubmit project for generating final test results message.
//
// For more details, see comments in result.go.
func writeTestStatusFile(ctx *util.Context, results map[string]*testutil.TestResult) error {
	// Get the file path.
	workspace, fileName := os.Getenv("WORKSPACE"), fmt.Sprintf("status_%s.json", strings.Replace(testFlag, "-", "_", -1))
	statusFilePath := ""
	if workspace == "" {
		statusFilePath = filepath.Join(os.Getenv("HOME"), "tmp", testFlag, fileName)
	} else {
		statusFilePath = filepath.Join(workspace, fileName)
	}

	// Write to file.
	bytes, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("Marshal(%v) failed: %v", results, err)
	}
	if err := ctx.Run().WriteFile(statusFilePath, bytes, os.FileMode(0644)); err != nil {
		return fmt.Errorf("WriteFile(%v) failed: %v", statusFilePath, err)
	}
	return nil
}

// checkMasterJobStatus returns the status of the presubmit-test master job.
func checkMasterJobStatus(ctx *util.Context) (_ string, e error) {
	jenkins := ctx.Jenkins(jenkinsHostFlag)
	getMasterJobStatusUri := fmt.Sprintf("job/%s/%d/api/json", presubmitTestFlag, jenkinsBuildNumberFlag)
	getMasterJobStatusRes, err := jenkins.Invoke("GET", getMasterJobStatusUri, url.Values{
		"token": {jenkinsTokenFlag},
	})
	if err != nil {
		return "", err
	}
	defer collect.Error(func() error { return getMasterJobStatusRes.Body.Close() }, &e)
	r := bufio.NewReader(getMasterJobStatusRes.Body)
	var status struct {
		Result string
	}
	if err := json.NewDecoder(r).Decode(&status); err != nil {
		return "", fmt.Errorf("Decode() %v", err)
	}
	return status.Result, nil
}

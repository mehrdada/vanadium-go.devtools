package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io/ioutil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"v.io/lib/cmdline"
	"v.io/x/devtools/lib/jenkins"
	"v.io/x/devtools/lib/testutil"
	"v.io/x/devtools/lib/util"
)

// cmdResult represents the 'result' command of the presubmit tool.
var cmdResult = &cmdline.Command{
	Name:  "result",
	Short: "Process and post test results.",
	Long: `
Result processes all the test statuses and results files collected from all the
presubmit test configuration builds, creates a result summary, and posts the
summary back to the corresponding Gerrit review thread.
`,
	Run: runResult,
}

type testResultInfo struct {
	Result           testutil.TestResult
	TestName         string
	SlaveLabel       string
	Timestamp        int64
	PostSubmitResult string
}

// runResult implements the 'result' subcommand.
//
// In the new presubmit "master" job, the collected results related files are
// organized using the following structure:
//
// ${WORKSPACE}
// ├── root
// └── test_results
//     ├── 45    (build number)
//     │    ├── L=linux-slave,TEST=vanadium-go-build
//     │    │   ├── status_vanadium_go_build.json
//     │    │   └─- tests_vanadium_go_build.xml
//     │    ├── L=linux-slave,TEST=vanadium-go-test
//     │    │   ├── status_vanadium_go_test.json
//     │    │   └─- tests_vanadium_go_test.xml
//     │    ├── L=mac-slave,TEST=vanadium-go-build
//     │    │   ├── status_vanadium_go_build.json
//     │    │   └─- tests_vanadium_go_build.xml
//     │    └── ...
//     ├── 46
//     ...
//
// The .json files record the test status (a testutil.TestResult object), and
// the .xml files are xUnit reports.
//
// Each individual presubmit test will generate the .json file and the .xml file
// at the end of their run, and the presubmit "master" job is configured to
// collect all those files and store them in the above directory structure.
func runResult(command *cmdline.Command, args []string) (e error) {
	ctx := util.NewContextFromCommand(command, !noColorFlag, dryRunFlag, verboseFlag)

	// Find all status files and store their paths in a slice.
	workspaceDir := os.Getenv("WORKSPACE")
	curTestResultsDir := filepath.Join(workspaceDir, "test_results", fmt.Sprintf("%d", jenkinsBuildNumberFlag))
	statusFiles := []string{}
	filepath.Walk(curTestResultsDir, func(path string, info os.FileInfo, err error) error {
		fileName := info.Name()
		if strings.HasPrefix(fileName, "status_") && strings.HasSuffix(fileName, ".json") {
			statusFiles = append(statusFiles, path)
		}
		return nil
	})

	// Read status files and add them to the "results" map below.
	sort.Strings(statusFiles)
	testResults := []testResultInfo{}
	for _, statusFile := range statusFiles {
		bytes, err := ioutil.ReadFile(statusFile)
		if err != nil {
			return fmt.Errorf("ReadFile(%v) failed: %v", statusFile, err)
		}
		var curResult testResultInfo
		if err := json.Unmarshal(bytes, &curResult); err != nil {
			return fmt.Errorf("Unmarshal() failed: %v", err)
		}
		testResults = append(testResults, curResult)
	}

	// Post results.
	refs := strings.Split(reviewTargetRefsFlag, ":")
	if err := postTestReport(ctx, testResults, refs); err != nil {
		return err
	}

	return nil
}

// postTestReport generates a test report and posts it to Gerrit.
func postTestReport(ctx *util.Context, testResults []testResultInfo, refs []string) (e error) {
	// Do not post a test report if no tests were run.
	if len(testResults) == 0 {
		return nil
	}

	printf(ctx.Stdout(), "### Preparing report\n")
	var report bytes.Buffer

	if reportFailedPresubmitBuild(ctx, &report) {
		return nil
	}

	// Report possible merge conflicts.
	// If any merge conflicts are found and reported, don't generate any
	// further report.
	if reportMergeConflicts(ctx, testResults, refs) {
		return nil
	}

	reportBuildCop(ctx, &report)

	failedTestNames := []string{}
	numNewFailures := 0
	if failedTestNames = reportTestResultsSummary(ctx, testResults, &report); len(failedTestNames) != 0 {
		// Report failed test cases grouped by failure types.
		var err error
		if numNewFailures, err = reportFailedTestCases(ctx, testResults, &report); err != nil {
			return err
		}
	}

	reportUsefulLinks(&report, failedTestNames)

	printf(ctx.Stdout(), "### Posting test results to Gerrit\n")
	success := numNewFailures == 0
	if err := postMessage(ctx, report.String(), refs, success); err != nil {
		return err
	}
	return nil
}

// reportFailedPresubmitBuild reports a failed presubmit build.
// It returns whether the presubmit build failed or not.
//
// In theory, a failed presubmit master build won't even execute the
// result reporting step (the cmdResult command implemented in this file),
// but just in case.
func reportFailedPresubmitBuild(ctx *util.Context, report *bytes.Buffer) bool {
	jenkins, err := ctx.Jenkins(jenkinsHostFlag)
	if err != nil {
		fmt.Fprintf(ctx.Stderr(), "%v\n", err)
		return false
	}

	masterJobInfo, err := jenkins.BuildInfo(presubmitTestJobFlag, jenkinsBuildNumberFlag)
	if err != nil {
		fmt.Fprintf(ctx.Stderr(), "%v\n", err)
		return false
	}
	printf(ctx.Stdout(), masterJobInfo.Result)
	if masterJobInfo.Result == "FAILURE" {
		fmt.Fprintf(report, "SOME TESTS FAILED TO RUN.\nRetrying...\n")
		return true
	}
	return false
}

// reportMergeConflicts posts a review about possible merge conflicts.
// It returns whether any merge conflicts are found in the given testResults.
func reportMergeConflicts(ctx *util.Context, testResults []testResultInfo, refs []string) bool {
	for _, resultInfo := range testResults {
		if resultInfo.Result.Status == testutil.TestFailedMergeConflict {
			message := fmt.Sprintf(mergeConflictMessageTmpl, resultInfo.Result.MergeConflictCL)
			if err := postMessage(ctx, message, refs, false); err != nil {
				printf(ctx.Stderr(), "%v\n", err)
			}
			return true
		}
	}
	return false
}

// reportBuildCop reports current vanadium build cop.
func reportBuildCop(ctx *util.Context, report *bytes.Buffer) {
	buildCop, err := util.BuildCop(ctx, time.Now())
	if err != nil {
		fmt.Fprintf(ctx.Stderr(), "%v\n", err)
	} else {
		fmt.Fprintf(report, "\nCurrent Build Cop: %s\n\n", buildCop)
	}
}

// reportTestResultsSummary populates the given buffer with a test
// results summary (one transition for each test) and returns a list of
// failed tests.
func reportTestResultsSummary(ctx *util.Context, testResults []testResultInfo, report *bytes.Buffer) []string {
	fmt.Fprintf(report, "Test results:\n")
	failedTestNames := []string{}
	for _, resultInfo := range testResults {
		name := resultInfo.TestName
		result := resultInfo.Result
		slaveLabel := resultInfo.SlaveLabel
		if result.Status == testutil.TestSkipped {
			fmt.Fprintf(report, "skipped %v\n", name)
			continue
		}

		// Get the status of the last completed build for this Jenkins test.
		lastStatus := getPostSubmitBuildStatus(ctx, name, slaveLabel, resultInfo.Timestamp)
		lastStatusString := "✖"
		switch lastStatus {
		case unknownStatus:
			lastStatusString = "?"
		case "SUCCESS":
			lastStatusString = "✔"
		}

		var curStatusString string
		if result.Status == testutil.TestPassed {
			curStatusString = "✔"
		} else {
			failedTestNames = append(failedTestNames, name)
			curStatusString = "✖"
		}

		nameString := name
		// Remove "-slave" from the label simplicity.
		if isMultiConfigurationJob(name) {
			slaveLabel = strings.Replace(slaveLabel, "-slave", "", -1)
			nameString += fmt.Sprintf(" [%s]", slaveLabel)
		}
		fmt.Fprintf(report, "%s ➔ %s: %s", lastStatusString, curStatusString, nameString)

		if result.Status == testutil.TestTimedOut {
			timeoutValue := testutil.DefaultTestTimeout
			if result.TimeoutValue != 0 {
				timeoutValue = result.TimeoutValue
			}
			fmt.Fprintf(report, " [TIMED OUT after %s]\n", timeoutValue)
		} else {
			fmt.Fprintf(report, "\n")
		}
	}
	return failedTestNames
}

// getPostSubmitBuildResultBefore gets the status of the given postsubmit
// test's result that ran before the given timestamp.
func getPostSubmitBuildStatus(ctx *util.Context, jobName, slaveLabel string, timestamp int64) string {
	fmt.Fprintf(ctx.Stdout(), "Getting postsubmit build status for %s-%s before timestamp %d...\n", jobName, slaveLabel, timestamp)
	buildInfo, err := lastCompletedBuildStatus(ctx, jobName, slaveLabel)
	if err != nil {
		testutil.Fail(ctx, "%v\n", err)
		return unknownStatus
	}
	curIdStr := buildInfo.Id
	curId, err := strconv.Atoi(curIdStr)
	if err != nil {
		testutil.Fail(ctx, "Atoi(%v) failed: %v\n", curIdStr, err)
		return unknownStatus
	}
	jenkins, err := ctx.Jenkins(jenkinsHostFlag)
	for i := curId; i >= 0; i-- {
		fmt.Fprintf(ctx.Stdout(), "Checking build %d...\n", i)
		buildSpec := fmt.Sprintf("%s/%d", jobName, i)
		if isMultiConfigurationJob(jobName) {
			buildSpec = fmt.Sprintf("%s/L=%s/%d", jobName, slaveLabel, i)
		}
		curBuildInfo, err := jenkins.BuildInfoForSpec(buildSpec)
		if err != nil {
			testutil.Fail(ctx, "%v\n", err)
			return unknownStatus
		}
		if curBuildInfo.Timestamp > timestamp {
			continue
		}
		testutil.Pass(ctx, "Got build status of build %d: %s\n", i, buildInfo.Result)
		return buildInfo.Result
	}
	return unknownStatus
}

// All the multi-configuration Jenkins jobs.
var multiConfigurationJobs = map[string]struct{}{
	"third_party-go-test":       struct{}{},
	"vanadium-go-build":         struct{}{},
	"vanadium-go-test":          struct{}{},
	"vanadium-integration-test": struct{}{},
}

// isMultiConfigurationJobs checks whether the given job is a
// multi-configuration job on Jenkins.
func isMultiConfigurationJob(jobName string) bool {
	_, ok := multiConfigurationJobs[jobName]
	return ok
}

// lastCompletedBuildStatus gets the status of the last completed
// build for a given jenkins test.
func lastCompletedBuildStatus(ctx *util.Context, jobName string, slaveLabel string) (*jenkins.BuildInfo, error) {
	jenkins, err := ctx.Jenkins(jenkinsHostFlag)
	if err != nil {
		return nil, err
	}

	buildSpec := fmt.Sprintf("%s/lastCompletedBuild", jobName)
	if isMultiConfigurationJob(jobName) {
		buildSpec = fmt.Sprintf("%s/L=%s/lastCompletedBuild", jobName, slaveLabel)
	}
	buildInfo, err := jenkins.BuildInfoForSpec(buildSpec)
	if err != nil {
		return nil, err
	}
	return buildInfo, nil
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

// reportFailedTestCasesByFailureTypes reports failed test cases grouped by
// failure types: new failures, known failures, and fixed failures.
func reportFailedTestCases(ctx *util.Context, testResults []testResultInfo, report *bytes.Buffer) (int, error) {
	// Get groups.
	groups, err := genFailedTestCasesGroupsForAllTests(ctx, testResults)
	if err != nil {
		return -1, err
	}

	// Generate links for all groups.
	for _, failureType := range []failureType{newFailure, knownFailure, fixedFailure} {
		failedTestCaseInfos, ok := groups[failureType]
		if !ok || len(failedTestCaseInfos) == 0 {
			continue
		}
		failureTypeStr := failureType.String()
		if len(failedTestCaseInfos) > 1 {
			failureTypeStr += "S"
		}
		curLinks := []string{}
		for _, testCase := range failedTestCaseInfos {
			className := testCase.className
			testCaseName := testCase.testCaseName
			curLink := genTestResultLink(className, testCaseName, testCase.seenTestsCount, testCase.testName, testCase.slaveLabel)
			curLinks = append(curLinks, curLink)
		}
		fmt.Fprintf(report, "\n%s:\n%s\n\n", failureTypeStr, strings.Join(curLinks, "\n"))
	}

	return len(groups[newFailure]), nil
}

type failedTestCaseInfo struct {
	className      string
	testCaseName   string
	seenTestsCount int
	testName       string
	slaveLabel     string
}

type failedTestCasesGroups map[failureType][]failedTestCaseInfo

// genFailedTestCasesGroupsForAllTests iterate all tests from the given
// testResults, compares the presubmit failed test cases (read from the given
// xUnit report) with the postsubmit failed test cases, and groups the failed
// tests into three groups: new failures, known failures, and fixed failures.
// Each group has a slice of failedTestLinkInfo which is used to generate links
// to Jenkins report pages.
func genFailedTestCasesGroupsForAllTests(ctx *util.Context, testResults []testResultInfo) (failedTestCasesGroups, error) {
	groups := failedTestCasesGroups{}

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
		testName := testResult.TestName
		slaveLabel := testResult.SlaveLabel
		// For a given test script this-is-a-test.sh, its test
		// report file is: tests_this_is_a_test.xml.
		xUnitReportFileName := fmt.Sprintf("tests_%s.xml", strings.Replace(testName, "-", "_", -1))
		// The collected xUnit test report is located at:
		// $WORKSPACE/test_results/$buildNumber/L=$slaveLabel,TEST=$testName/tests_xxx.xml
		//
		// See more details in result.go.
		xUnitReportFile := filepath.Join(
			os.Getenv("WORKSPACE"),
			"test_results",
			fmt.Sprintf("%d", jenkinsBuildNumberFlag),
			fmt.Sprintf("L=%s,TEST=%s", slaveLabel, testName),
			xUnitReportFileName)
		bytes, err := ioutil.ReadFile(xUnitReportFile)
		if err != nil {
			// It is normal that certain tests don't have report available.
			printf(ctx.Stderr(), "ReadFile(%v) failed: %v\n", xUnitReportFile, err)
			continue
		}

		// Get the failed test cases from the corresponding postsubmit Jenkins job
		// to compare with the presubmit failed tests.
		postsubmitFailedTestCases, err := getFailedTestCases(ctx, testName, slaveLabel)
		if err != nil {
			// postsubmitFailedTestCases would be empty on errors, which is fine.
			printf(ctx.Stderr(), "%v\n", err)
		}
		curFailedTestCasesGroups, err := genFailedTestCasesGroupsForOneTest(ctx, testResult, bytes, seenTests, postsubmitFailedTestCases)
		if err != nil {
			printf(ctx.Stderr(), "%v\n", err)
			continue
		}
		for curFailureType, curFailedTestCaseInfos := range *curFailedTestCasesGroups {
			groups[curFailureType] = append(groups[curFailureType], curFailedTestCaseInfos...)
		}
	}
	return groups, nil
}

// getFailedTestCases gets a list of failed test cases from the most
// recent build of the given Jenkins test.
func getFailedTestCases(ctx *util.Context, jobName string, slaveLabel string) (_ []jenkins.TestCase, e error) {
	jenkinsObj, err := ctx.Jenkins(jenkinsHostFlag)
	if err != nil {
		return []jenkins.TestCase{}, err
	}

	buildSpec := fmt.Sprintf("%s/lastCompletedBuild", jobName)
	if isMultiConfigurationJob(jobName) {
		buildSpec = fmt.Sprintf("%s/L=%s/lastCompletedBuild", jobName, slaveLabel)
	}
	return jenkinsObj.FailedTestCasesForBuildSpec(buildSpec)
}

type testSuites struct {
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

// genFailedTestCasesGroupsForOneTest generates groups for failed tests.
// See comments of genFailedTestsGroupsForAllTests.
func genFailedTestCasesGroupsForOneTest(ctx *util.Context, testResult testResultInfo, presubmitXUnitReport []byte, seenTests map[string]int, postsubmitFailedTestCases []jenkins.TestCase) (*failedTestCasesGroups, error) {
	slaveLabel := testResult.SlaveLabel
	testName := testResult.TestName

	// Parse xUnit report of the presubmit test.
	suites := testSuites{}
	if err := xml.Unmarshal(presubmitXUnitReport, &suites); err != nil {
		return nil, fmt.Errorf("Unmarshal(%v) failed: %v", string(presubmitXUnitReport), err)
	}

	groups := failedTestCasesGroups{}
	curFailedTestCases := []jenkins.TestCase{}
	for _, curTestSuite := range suites.Testsuites {
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
				linkInfo := failedTestCaseInfo{
					className:      curTestCase.Classname,
					testCaseName:   curTestCase.Name,
					seenTestsCount: seenTests[testKey],
					testName:       testName,
					slaveLabel:     slaveLabel,
				}
				// Determine whether the curTestCase is a new failure or not.
				isNewFailure := true
				for _, postsubmitFailedTestCase := range postsubmitFailedTestCases {
					if curTestCase.Classname == postsubmitFailedTestCase.ClassName && curTestCase.Name == postsubmitFailedTestCase.Name {
						isNewFailure = false
						break
					}
				}
				if isNewFailure {
					groups[newFailure] = append(groups[newFailure], linkInfo)
				} else {
					groups[knownFailure] = append(groups[knownFailure], linkInfo)
				}
				curFailedTestCases = append(curFailedTestCases, jenkins.TestCase{
					ClassName: curTestCase.Classname,
					Name:      curTestCase.Name,
				})
			}
		}
	}
	// Populate fixed failure group.
	for _, postsubmitFailedTestCase := range postsubmitFailedTestCases {
		fixed := true
		for _, curFailedTestCase := range curFailedTestCases {
			if postsubmitFailedTestCase.Equal(curFailedTestCase) {
				fixed = false
				break
			}
		}
		if fixed {
			groups[fixedFailure] = append(groups[fixedFailure], failedTestCaseInfo{
				className:    postsubmitFailedTestCase.ClassName,
				testCaseName: postsubmitFailedTestCase.Name,
			})
		}
	}
	return &groups, nil
}

// genTestResultLink generates a link failed test case's report page on Jenkins.
func genTestResultLink(className, testCaseName string, suffix int, testName, slaveLabel string) string {
	packageName := "(root)"
	testFullName := genTestFullName(className, testCaseName)
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

func genTestFullName(className, testName string) string {
	testFullName := fmt.Sprintf("%s.%s", className, testName)
	// Replace the period "." in testFullName with
	// "::" to stop gmail from turning it into a
	// link automatically.
	return strings.Replace(testFullName, ".", "::", -1)
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

// reportUsefulLinks reports useful links:
// - Current presubmit-test master status page.
// - Retry failed tests only.
// - Retry current build.
func reportUsefulLinks(report *bytes.Buffer, failedTestNames []string) {
	fmt.Fprintf(report, "\nMore details at:\n%s/%s/%d/\n", jenkinsBaseJobUrl, presubmitTestJobFlag, jenkinsBuildNumberFlag)
	if len(failedTestNames) > 0 {
		// Generate link to retry failed tests only.
		link := genStartPresubmitBuildLink(reviewTargetRefsFlag, projectsFlag, strings.Join(failedTestNames, " "))
		fmt.Fprintf(report, "\nTo re-run FAILED TESTS ONLY without uploading a new patch set:\n(click Proceed button on the next screen)\n%s\n", link)

		// Generate link to retry the whole presubmit test.
		link = genStartPresubmitBuildLink(reviewTargetRefsFlag, projectsFlag, os.Getenv("TESTS"))
		fmt.Fprintf(report, "\nTo re-run presubmit tests without uploading a new patch set:\n(click Proceed button on the next screen)\n%s\n", link)
	}
}

// postMessage posts the given message to Gerrit.
func postMessage(ctx *util.Context, message string, refs []string, success bool) error {
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

	// Construct and post the reviews.
	refsUsingVerifiedLabel, err := getRefsUsingVerifiedLabel(ctx, gerritCred)
	if err != nil {
		return err
	}
	value := "1"
	if !success {
		value = "-" + value
	}
	gerrit := ctx.Gerrit(gerritBaseUrlFlag, gerritCred.username, gerritCred.password)
	for _, ref := range refs {
		labels := map[string]string{}
		if _, ok := refsUsingVerifiedLabel[ref]; ok {
			labels["Verified"] = value
		}
		if err := gerrit.PostReview(ref, message, labels); err != nil {
			return err
		}
		testutil.Pass(ctx, "review posted for %q with labels %v.\n", ref, labels)
	}
	return nil
}

func getRefsUsingVerifiedLabel(ctx *util.Context, gerritCred credential) (map[string]struct{}, error) {
	// Query all open CLs.
	gerrit := ctx.Gerrit(gerritBaseUrlFlag, gerritCred.username, gerritCred.password)
	cls, err := gerrit.Query(defaultQueryString)
	if err != nil {
		return nil, err
	}

	// Identify the refs that use the "Verified" label.
	ret := map[string]struct{}{}
	for _, cl := range cls {
		if _, ok := cl.Labels["Verified"]; ok {
			ret[cl.Reference()] = struct{}{}
		}
	}

	return ret, nil
}

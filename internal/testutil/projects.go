package testutil

import (
	"path/filepath"
	"time"

	"v.io/x/devtools/internal/collect"
	"v.io/x/devtools/internal/runutil"
	"v.io/x/devtools/internal/tool"
	"v.io/x/devtools/internal/util"
	"v.io/x/devtools/internal/xunit"
)

const (
	defaultProjectTestTimeout = 5 * time.Minute
)

// runProjectTest is a helper for running project tests.
func runProjectTest(ctx *tool.Context, testName, projectName, target string, env map[string]string, profiles []string) (_ *TestResult, e error) {
	// Initialize the test.
	cleanup, err := initTest(ctx, testName, profiles)
	if err != nil {
		return nil, err
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	// Navigate to project directory.
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "projects", projectName)
	if err := ctx.Run().Chdir(testDir); err != nil {
		return nil, err
	}

	// Clean.
	if err := ctx.Run().Command("make", "clean"); err != nil {
		return nil, err
	}

	// Set environment from the env argument map.
	opts := ctx.Run().Opts()
	for k, v := range env {
		opts.Env[k] = v
	}

	// Run the tests.
	if err := ctx.Run().TimedCommandWithOpts(defaultProjectTestTimeout, opts, "make", target); err != nil {
		if err == runutil.CommandTimedOutErr {
			return &TestResult{
				Status:       TestTimedOut,
				TimeoutValue: defaultProjectTestTimeout,
			}, nil
		} else {
			return nil, internalTestError{err, "Make " + target}
		}
	}

	return &TestResult{Status: TestPassed}, nil
}

// vanadiumChatShellTest runs the tests for the chat shell client.
func vanadiumChatShellTest(ctx *tool.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	return runProjectTest(ctx, testName, "chat", "test-shell", nil, nil)
}

// vanadiumChatWebTest runs the tests for the chat web client.
func vanadiumChatWebTest(ctx *tool.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	return runProjectTest(ctx, testName, "chat", "test-web", nil, []string{"web"})
}

// vanadiumNamespaceBrowserTest runs the tests for the Vanadium namespace browser.
func vanadiumNamespaceBrowserTest(ctx *tool.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	env := map[string]string{
		"XUNIT_OUTPUT_FILE": xunit.ReportPath(testName),
	}

	return runProjectTest(ctx, testName, "namespace_browser", "test", env, []string{"web"})
}

// vanadiumPipe2BrowserTest runs the tests for pipe2browser.
func vanadiumPipe2BrowserTest(ctx *tool.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	return runProjectTest(ctx, testName, "pipe2browser", "test", nil, []string{"web"})
}
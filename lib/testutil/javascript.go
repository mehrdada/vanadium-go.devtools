package testutil

import (
	"path/filepath"
	"time"

	"v.io/x/devtools/lib/collect"
	"v.io/x/devtools/lib/runutil"
	"v.io/x/devtools/lib/util"
)

const (
	defaultJSTestTimeout = 10 * time.Minute
)

// runJSTest is a harness for executing javascript tests.
func runJSTest(ctx *util.Context, testName, testDir, target string, cleanFn func() error, env map[string]string) (_ *TestResult, e error) {
	// Initialize the test.
	cleanup, err := initTest(ctx, testName, []string{"web"})
	if err != nil {
		return nil, internalTestError{err, "Init"}
	}
	defer collect.Error(func() error { return cleanup() }, &e)

	// Navigate to the target directory.
	if err := ctx.Run().Chdir(testDir); err != nil {
		return nil, err
	}

	// Clean up after previous instances of the test.
	opts := ctx.Run().Opts()
	for key, value := range env {
		opts.Env[key] = value
	}
	if err := ctx.Run().CommandWithOpts(opts, "make", "clean"); err != nil {
		return nil, err
	}
	if cleanFn != nil {
		if err := cleanFn(); err != nil {
			return nil, err
		}
	}

	// Run the test target.
	if err := ctx.Run().TimedCommandWithOpts(defaultJSTestTimeout, opts, "make", target); err != nil {
		if err == runutil.CommandTimedOutErr {
			return &TestResult{
				Status:       TestTimedOut,
				TimeoutValue: defaultJSTestTimeout,
			}, nil
		} else {
			return nil, internalTestError{err, "Make " + target}
		}
	}

	return &TestResult{Status: TestPassed}, nil
}

// vanadiumJSBuildExtension tests the vanadium javascript build extension.
func vanadiumJSBuildExtension(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "extension/veyron.zip"
	return runJSTest(ctx, testName, testDir, target, nil, nil)
}

// vanadiumJSDoc (re)generates the content of the vanadium javascript
// documentation server.
func vanadiumJSDoc(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "docs"
	webDir, jsDocDir := "/var/www/jsdoc", filepath.Join(testDir, "docs")
	cleanFn := func() error {
		if err := ctx.Run().RemoveAll(webDir); err != nil {
			return err
		}
		return nil
	}
	result, err := runJSTest(ctx, testName, testDir, target, cleanFn, nil)
	if err != nil {
		return nil, err
	}
	// Move generated js documentation to the web server directory.
	if err := ctx.Run().Rename(jsDocDir, webDir); err != nil {
		return nil, err
	}
	return result, nil
}

// vanadiumJSBrowserIntegration runs the vanadium javascript integration test in a browser environment using nacl plugin.
func vanadiumJSBrowserIntegration(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "test-integration-browser"
	env := map[string]string{}
	setCommonJSEnv(env)
	env["BROWSER_OUTPUT"] = XUnitReportPath(testName)
	return runJSTest(ctx, testName, testDir, target, nil, env)
}

// vanadiumJSNodeIntegration runs the vanadium javascript integration test in NodeJS environment using wspr.
func vanadiumJSNodeIntegration(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "test-integration-node"
	env := map[string]string{}
	setCommonJSEnv(env)
	env["NODE_OUTPUT"] = XUnitReportPath(testName)
	return runJSTest(ctx, testName, testDir, target, nil, env)
}

// vanadiumJSUnit runs the vanadium javascript unit test.
func vanadiumJSUnit(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "test-unit"
	env := map[string]string{}
	setCommonJSEnv(env)
	env["NODE_OUTPUT"] = XUnitReportPath(testName)
	return runJSTest(ctx, testName, testDir, target, nil, env)
}

// vanadiumJSVdl runs the vanadium javascript vdl test.
func vanadiumJSVdl(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "test-vdl"
	env := map[string]string{}
	setCommonJSEnv(env)
	env["NODE_OUTPUT"] = XUnitReportPath(testName)
	return runJSTest(ctx, testName, testDir, target, nil, env)
}

// vanadiumJSVom runs the vanadium javascript vom test.
func vanadiumJSVom(ctx *util.Context, testName string, _ ...TestOpt) (*TestResult, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	testDir := filepath.Join(root, "release", "javascript", "core")
	target := "test-vom"
	env := map[string]string{}
	setCommonJSEnv(env)
	env["NODE_OUTPUT"] = XUnitReportPath(testName)
	return runJSTest(ctx, testName, testDir, target, nil, env)
}

func setCommonJSEnv(env map[string]string) {
	env["XUNIT"] = "true"
}

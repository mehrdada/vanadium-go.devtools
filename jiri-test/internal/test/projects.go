// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package test

import (
	"path/filepath"
	"time"

	"v.io/jiri"
	"v.io/x/devtools/internal/test"
	"v.io/x/devtools/internal/xunit"
)

const (
	defaultProjectTestTimeout = 15 * time.Minute
)

func runMakefileTestWithNacl(jirix *jiri.X, testName, testDir, target string, env map[string]string, profiles []string, timeout time.Duration) (_ *test.Result, e error) {
	if err := installExtraDeps(jirix, testName, []string{"v23:nacl"}, "amd64p32-nacl"); err != nil {
		return nil, err
	}
	return runMakefileTest(jirix, testName, testDir, target, env, profiles, timeout)
}

// vanadiumBakuTest runs the tests for the Baku toolkit.
// NOTE: A new file for Baku tests should be added if this becomes more
// complicated than simply running `make test`.
func vanadiumBakuTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "baku")
	return runMakefileTest(jirix, testName, testDir, "test", nil, nil, defaultProjectTestTimeout)
}

// vanadiumBrowserTest runs the tests for the Vanadium browser.
func vanadiumBrowserTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	env := map[string]string{
		"XUNIT_OUTPUT_FILE": xunit.ReportPath(testName),
	}
	testDir := filepath.Join(jirix.Root, "release", "projects", "browser")
	return runMakefileTestWithNacl(jirix, testName, testDir, "test", env, []string{"v23:nodejs"}, defaultProjectTestTimeout)
}

// vanadiumBrowserTestWeb runs the ui tests for the Vanadium browser.
func vanadiumBrowserTestWeb(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "browser")
	return runMakefileTestWithNacl(jirix, testName, testDir, "test-ui", nil, []string{"v23:nodejs"}, defaultProjectTestTimeout)
}

// vanadiumChatShellTest runs the tests for the chat shell client.
func vanadiumChatShellTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "chat")
	return runMakefileTest(jirix, testName, testDir, "test-shell", nil, nil, defaultProjectTestTimeout)
}

// vanadiumChatWebTest runs the tests for the chat web client.
func vanadiumChatWebTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "chat")
	return runMakefileTestWithNacl(jirix, testName, testDir, "test-web", nil, []string{"v23:nodejs"}, defaultProjectTestTimeout)
}

// vanadiumChatWebUITest runs the ui tests for the chat web client.
func vanadiumChatWebUITest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "chat")
	return runMakefileTestWithNacl(jirix, testName, testDir, "test-ui", nil, []string{"v23:nodejs"}, defaultProjectTestTimeout)
}

// vanadiumPipe2BrowserTest runs the tests for pipe2browser.
func vanadiumPipe2BrowserTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "pipe2browser")
	return runMakefileTestWithNacl(jirix, testName, testDir, "test", nil, []string{"v23:nodejs"}, defaultProjectTestTimeout)
}

// vanadiumCroupierTestUnit runs the unit tests for the croupier example application.
// Note: This test requires the "with_flutter" manifest, or a Flutter checkout in ${JIRI_ROOT}.
func vanadiumCroupierTestUnit(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "croupier")
	return runMakefileTest(jirix, testName, testDir, "test-unit", nil, []string{"v23:dart"}, defaultProjectTestTimeout)
}

// vanadiumCroupierTestUnitGo runs the Go unit tests for the croupier example application.
func vanadiumCroupierTestUnitGo(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "croupier", "go")
	return runMakefileTest(jirix, testName, testDir, "test", nil, nil, defaultProjectTestTimeout)
}

// vanadiumReaderTest runs the tests for the reader example application.
func vanadiumReaderTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "reader")
	return runMakefileTest(jirix, testName, testDir, "test", nil, []string{"v23:base"}, defaultProjectTestTimeout)
}

// vanadiumTodosAndroidTest confirms that the build succeeds and its tests pass.
func vanadiumTodosAndroidTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	return runJavaTest(jirix, testName, []string{"release", "projects", "todos"}, []string{"clean", "build"})
}

// vanadiumTravelTest runs the tests for the travel example application.
func vanadiumTravelTest(jirix *jiri.X, testName string, _ ...Opt) (*test.Result, error) {
	testDir := filepath.Join(jirix.Root, "release", "projects", "travel")
	return runMakefileTest(jirix, testName, testDir, "test", nil, []string{"v23:nodejs"}, defaultProjectTestTimeout)
}

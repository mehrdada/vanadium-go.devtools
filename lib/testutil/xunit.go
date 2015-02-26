package testutil

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"v.io/x/devtools/lib/util"
)

type testSuites struct {
	Suites  []testSuite `xml:"testsuite"`
	XMLName xml.Name    `xml:"testsuites"`
}

type testSuite struct {
	Name     string     `xml:"name,attr"`
	Cases    []testCase `xml:"testcase"`
	Errors   int        `xml:"errors,attr"`
	Failures int        `xml:"failures,attr"`
	Skip     int        `xml:"skip,attr"`
	Tests    int        `xml:"tests,attr"`
}

type testCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Errors    []testError   `xml:"error"`
	Failures  []testFailure `xml:"failure"`
	Time      string        `xml:"time,attr"`
	Skipped   []string      `xml:"skipped"`
}

type testError struct {
	Message string `xml:"message,attr"`
	Data    string `xml:",chardata"`
}

type testFailure struct {
	Message string `xml:"message,attr"`
	Data    string `xml:",chardata"`
}

// xUnitReportPath returns the path to the xUnit file.
//
// TODO(jsimsa): Once all Jenkins shell test scripts are ported to Go,
// change the filename to xunit_report_<testName>.xml.
func XUnitReportPath(testName string) string {
	workspace, fileName := os.Getenv("WORKSPACE"), fmt.Sprintf("tests_%s.xml", strings.Replace(testName, "-", "_", -1))
	if workspace == "" {
		return filepath.Join(os.Getenv("HOME"), "tmp", testName, fileName)
	} else {
		return filepath.Join(workspace, fileName)
	}
}

// testSuiteFromGoTestOutput reads data from the given input, assuming
// it contains test results generated by "go test -v", and returns it
// as an in-memory data structure.
func testSuiteFromGoTestOutput(ctx *util.Context, testOutput io.Reader) (*testSuite, error) {
	root, err := util.VanadiumRoot()
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(root, "third_party", "go", "bin", "go2xunit")
	var out bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stdin = testOutput
	opts.Stdout = &out
	if err := ctx.Run().CommandWithOpts(opts, bin); err != nil {
		return nil, err
	}
	var suite testSuite
	if err := xml.Unmarshal(out.Bytes(), &suite); err != nil {
		return nil, fmt.Errorf("Unmarshal() failed: %v\n%v", err, out.String())
	}
	return &suite, nil
}

// createXUnitReport generates an xUnit report using the given test
// suites.
func createXUnitReport(ctx *util.Context, testName string, suites []testSuite) error {
	// Change all go package names from v.io/xyz to v_io.xyz so Jenkins won't
	// split it at the wrong "." and show "v" as the "Package" in test reports.
	for i, s := range suites {
		if strings.HasPrefix(s.Name, "v.io/") {
			s.Name = strings.Replace(s.Name, "v.io/", "v_io.", 1)
			suites[i] = s
		}
		cases := s.Cases
		for j, c := range cases {
			if strings.HasPrefix(c.Classname, "v.io/") {
				c.Classname = strings.Replace(c.Classname, "v.io/", "v_io.", 1)
				cases[j] = c
			}
		}
		suites[i].Cases = cases
	}
	result := testSuites{Suites: suites}
	bytes, err := xml.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("MarshalIndent(%v) failed: %v", result, err)
	}
	if err := ctx.Run().WriteFile(XUnitReportPath(testName), bytes, os.FileMode(0644)); err != nil {
		return fmt.Errorf("WriteFile(%v) failed: %v", XUnitReportPath(testName), err)
	}
	return nil
}

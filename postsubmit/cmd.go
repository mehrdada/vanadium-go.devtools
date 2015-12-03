// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The following enables go generate to generate the doc.go file.
//go:generate go run $JIRI_ROOT/release/go/src/v.io/x/lib/cmdline/testdata/gendoc.go .

package main

import (
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"v.io/jiri/jiri"
	"v.io/jiri/project"
	"v.io/jiri/tool"
	"v.io/jiri/util"
	"v.io/x/devtools/internal/test"
	"v.io/x/lib/cmdline"
)

var (
	jenkinsHostFlag string
)

func init() {
	cmdRoot.Flags.StringVar(&jenkinsHostFlag, "host", "", "The Jenkins host. Presubmit will not send any CLs to an empty host.")

	tool.InitializeProjectFlags(&cmdPoll.Flags)
	tool.InitializeRunFlags(&cmdRoot.Flags)
}

func main() {
	cmdline.Main(cmdRoot)
}

// cmdRoot represents the root of the postsubmit tool.
var cmdRoot = &cmdline.Command{
	Name:  "postsubmit",
	Short: "Perform Vanadium postsubmit related functions",
	Long: `
Command postsubmit performs Vanadium postsubmit related functions.
`,
	Children: []*cmdline.Command{cmdPoll},
}

// cmdPoll represents the "poll" command of the postsubmit tool.
var cmdPoll = &cmdline.Command{
	Runner: jiri.RunnerFunc(runPoll),
	Name:   "poll",
	Short:  "Poll changes and start corresponding builds on Jenkins",
	Long:   "Poll changes and start corresponding builds on Jenkins.",
}

func runPoll(jirix *jiri.X, _ []string) error {
	// Get the latest snapshot file from the update history directory.
	var maxTime time.Time
	var latestSnapshot string
	findLatest := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if t := info.ModTime(); t.After(maxTime) {
			maxTime = t
			latestSnapshot = path
		}
		return nil
	}
	// TODO(toddw): Stop looking in the .update_history directory when the
	// transition to the new .jiri_root is complete.
	filepath.Walk(filepath.Join(jirix.Root, ".update_history"), findLatest)
	filepath.Walk(jirix.UpdateHistoryDir(), findLatest)

	// Get projects with new changes from the latest snapshots.
	snapshotFileBytes, err := ioutil.ReadFile(latestSnapshot)
	if err != nil {
		return fmt.Errorf("ReadFile() failed: %v", err)
	}
	projects, err := getChangedProjectsFromSnapshot(jirix, snapshotFileBytes)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Fprintf(jirix.Stdout(), "No changes.\n")
		return nil
	}
	fmt.Fprintf(jirix.Stdout(), "Projects with new changes:\n%s\n", strings.Join(projects, "\n"))

	// Identify the Jenkins tests that should be started.
	jenkinsTests, err := jenkinsTestsToStart(jirix, projects)
	if err != nil {
		return err
	}
	fmt.Fprintf(jirix.Stdout(), "\nJenkins tests to start:\n%s\n", strings.Join(jenkinsTests, "\n"))

	// Start Jenkins tests.
	fmt.Fprintf(jirix.Stdout(), "\nStarting new builds:\n")
	if err := startJenkinsTests(jirix, jenkinsTests); err != nil {
		return err
	}

	return nil
}

// getChangedProjectsFromSnapshot returns a slice of projects that
// have changes by comparing the revisions in the given snapshot with
// master branches.
func getChangedProjectsFromSnapshot(jirix *jiri.X, snapshotContent []byte) ([]string, error) {
	// Parse snapshot.
	snapshot := project.Manifest{}
	if err := xml.Unmarshal(snapshotContent, &snapshot); err != nil {
		return nil, fmt.Errorf("Unmarshal() failed: %v\n%v", err, string(snapshotContent))
	}

	// Use "git log" to detect changes for each project.
	//
	// TODO(jingjin, jsimsa): Add support for non-git projects.
	changedProjects := []string{}
	for _, project := range snapshot.Projects {
		switch project.Protocol {
		case "git":
			git := jirix.Git(tool.RootDirOpt(filepath.Join(jirix.Root, project.Path)))
			commits, err := git.Log("master", project.Revision, "")
			if err != nil {
				return nil, err
			}
			if len(commits) != 0 {
				changedProjects = append(changedProjects, project.Name)
			}
		}
	}
	return changedProjects, nil
}

// jenkinsTestsToStart returns a list of jenkins tests that need to be
// started based on the given projects.
func jenkinsTestsToStart(jirix *jiri.X, projects []string) ([]string, error) {
	// Parse tools config to get project-tests map.
	config, err := util.LoadConfig(jirix)
	if err != nil {
		return nil, err
	}

	// Get all Jenkins tests for the given projects.
	return config.ProjectTests(projects), nil
}

// startJenkinsTests uses Jenkins API to start a build to each of the
// given Jenkins tests.
func startJenkinsTests(jirix *jiri.X, tests []string) error {
	jenkins, err := jirix.Jenkins(jenkinsHostFlag)
	if err != nil {
		return err
	}

	for _, t := range tests {
		msg := fmt.Sprintf("add build to %q\n", t)
		if err := jenkins.AddBuild(t); err == nil {
			test.Pass(jirix.Context, "%s", msg)
		} else {
			test.Fail(jirix.Context, "%s", msg)
			fmt.Fprintf(jirix.Stderr(), "%v\n", err)
		}
	}
	return nil
}

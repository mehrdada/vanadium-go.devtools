package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tools/lib/cmd"
	"tools/lib/git"
)

const (
	rootEnv = "VEYRON_ROOT"
)

func selfUpdate(verbose bool, manifest, name string) error {
	root := os.Getenv(rootEnv)
	if root == "" {
		return fmt.Errorf("%v is not set", rootEnv)
	}
	url := "https://veyron.googlesource.com/tools"
	args := []string{fmt.Sprintf("-v=%v", verbose), "project", "update", "-manifest=" + manifest, url}
	if _, errOut, err := cmd.RunOutput(true, "veyron", args...); err != nil {
		return fmt.Errorf("%s", strings.Join(errOut, "\n"))
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("Getwd() failed: %v", err)
	}
	defer os.Chdir(wd)
	repo := filepath.Join(root, "tools")
	os.Chdir(repo)
	goScript := filepath.Join(root, "veyron", "scripts", "build", "go")
	git := git.New(verbose)
	count, err := git.CountCommits("HEAD", "")
	if err != nil {
		return err
	}
	output := filepath.Join(root, "bin", name)
	ldflags := fmt.Sprintf("-X tools/%v/impl.commitId %d", name, count)
	pkg := fmt.Sprintf("tools/%v", name)
	args = []string{"build", "-ldflags", ldflags, "-o", output, pkg}
	if _, errOut, err := cmd.RunOutput(true, goScript, args...); err != nil {
		return fmt.Errorf("%v tool update failed\n%v", name, strings.Join(errOut, "\n"))
	}
	return nil
}

// SelfUpdate updates the given tool to the latest version.
func SelfUpdate(verbose bool, manifest, name string) error {
	updateFn := func() error { return selfUpdate(verbose, manifest, name) }
	return cmd.Log(updateFn, "Updating tool %q", name)
}
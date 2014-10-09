package impl

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"tools/lib/cmdline"
	"tools/lib/gitutil"
	"tools/lib/hgutil"
	"tools/lib/runutil"
	"tools/lib/util"
)

var (
	branchesFlag bool
	gcFlag       bool
	manifestFlag string
	novdlFlag    bool
	platformFlag string
	verboseFlag  bool
)

func init() {
	cmdProjectList.Flags.BoolVar(&branchesFlag, "branches", false, "Show project branches.")
	cmdProjectUpdate.Flags.BoolVar(&gcFlag, "gc", false, "Garbage collect obsolete repositories.")
	cmdProjectUpdate.Flags.StringVar(&manifestFlag, "manifest", "absolute", "Name of the project manifest.")
	cmdSelfUpdate.Flags.StringVar(&manifestFlag, "manifest", "absolute", "Name of the project manifest.")
	cmdGo.Flags.BoolVar(&novdlFlag, "novdl", false, "Disable automatic generation of vdl files.")
	cmdXGo.Flags.BoolVar(&novdlFlag, "novdl", false, "Disable automatic generation of vdl files.")
	cmdEnv.Flags.StringVar(&platformFlag, "platform", "", "Target platform.")
	cmdRoot.Flags.BoolVar(&verboseFlag, "v", false, "Print verbose output.")
}

// Root returns a command that represents the root of the veyron tool.
func Root() *cmdline.Command {
	return cmdRoot
}

// cmdRoot represents the root of the veyron tool.
var cmdRoot = &cmdline.Command{
	Name:  "veyron",
	Short: "Tool for managing veyron development",
	Long:  "The veyron tool helps manage veyron development.",
	Children: []*cmdline.Command{
		cmdContributors,
		cmdProfile,
		cmdProject,
		cmdEnv,
		cmdRun,
		cmdGo,
		cmdGoExt,
		cmdXGo,
		cmdSelfUpdate,
		cmdVersion,
	},
}

// cmdContributors represents the 'contributors' command of the veyron tool.
var cmdContributors = &cmdline.Command{
	Run:   runContributors,
	Name:  "contributors",
	Short: "List veyron project contributors",
	Long: `
Lists veyron project contributors and the number of their
commits. Veyron projects to consider can be specified as an
argument. If no projects are specified, all veyron projects are
considered by default.
`,
	ArgsName: "<projects>",
	ArgsLong: "<projects> is a list of projects to consider.",
}

func runContributors(command *cmdline.Command, args []string) error {
	run := runutil.New(verboseFlag, command.Stdout())
	git, hg := gitutil.New(run), hgutil.New(run)
	projects, err := util.LocalProjects(git, hg)
	if err != nil {
		return err
	}
	repos := map[string]struct{}{}
	if len(args) != 0 {
		for _, arg := range args {
			repos[arg] = struct{}{}
		}
	} else {
		for name, _ := range projects {
			repos[name] = struct{}{}
		}
	}
	contributors := map[string]int{}
	for repo, _ := range repos {
		project, ok := projects[repo]
		if !ok {
			continue
		}
		if err := os.Chdir(project.Path); err != nil {
			return fmt.Errorf("Chdir(%v) failed: %v", project.Path, err)
		}
		lines, err := listCommitters(git)
		if err != nil {
			return err
		}
		for _, line := range lines {
			tokens := strings.SplitN(line, "\t", 2)
			n, err := strconv.Atoi(strings.TrimSpace(tokens[0]))
			if err != nil {
				return fmt.Errorf("Atoi(%v) failed: %v", tokens[0], err)
			}
			contributors[strings.TrimSpace(tokens[1])] += n
		}
	}
	names := []string{}
	for name, _ := range contributors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("%4d %v\n", contributors[name], name)
	}
	return nil
}

func listCommitters(git *gitutil.Git) ([]string, error) {
	branch, err := git.CurrentBranchName()
	if err != nil {
		return nil, err
	}
	stashed, err := git.Stash()
	if err != nil {
		return nil, err
	}
	if stashed {
		defer git.StashPop()
	}
	if err := git.CheckoutBranch("master", !gitutil.Force); err != nil {
		return nil, err
	}
	defer git.CheckoutBranch(branch, !gitutil.Force)
	return git.Committers()
}

// cmdSelfUpdate represents the 'selfupdate' command of the veyron tool.
var cmdSelfUpdate = &cmdline.Command{
	Run:   runSelfUpdate,
	Name:  "selfupdate",
	Short: "Update the veyron tool",
	Long:  "Download and install the latest version of the veyron tool.",
}

func runSelfUpdate(command *cmdline.Command, _ []string) error {
	return util.SelfUpdate(verboseFlag, command.Stdout(), manifestFlag, "veyron")
}

// cmdVersion represents the 'version' command of the veyron tool.
var cmdVersion = &cmdline.Command{
	Run:   runVersion,
	Name:  "version",
	Short: "Print version",
	Long:  "Print version of the veyron tool.",
}

// Version should be over-written during build:
//
// go build -ldflags "-X tools/veyron/impl.Version <version>" tools/veyron
var Version string = "manual-build"

func runVersion(command *cmdline.Command, _ []string) error {
	fmt.Fprintf(command.Stdout(), "veyron tool version %v\n", Version)
	return nil
}

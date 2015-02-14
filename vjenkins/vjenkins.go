// The following enables go generate to generate the doc.go file.
//go:generate go run $VANADIUM_ROOT/release/go/src/v.io/lib/cmdline/testdata/gendoc.go .
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"v.io/lib/cmdline"
	"v.io/tools/lib/util"
)

// TODO(jsimsa): Add tests by mocking out jenkins.
//
// TODO(jsimsa): Create a tools/lib/gcutil package that encapsulates
// the interaction with GCE and use it here and in the vcloud tool.
func main() {
	os.Exit(cmdVJenkins.Main())
}

var cmdVJenkins = &cmdline.Command{
	Name:     "vjenkins",
	Short:    "Vanadium command-line utility for interacting with Jenkins",
	Long:     "Vanadium command-line utility for interacting with Jenkins.",
	Children: []*cmdline.Command{cmdNode},
}

var cmdNode = &cmdline.Command{
	Name:     "node",
	Short:    "Manage Jenkins slave nodes",
	Long:     "Manage Jenkins slave nodes.",
	Children: []*cmdline.Command{cmdNodeCreate, cmdNodeDelete},
}

var cmdNodeCreate = &cmdline.Command{
	Run:   runNodeCreate,
	Name:  "create",
	Short: "Create Jenkins slave nodes",
	Long: `
Create Jenkins nodes. Uses the Jenkins REST API to create new slave nodes.
`,
	ArgsName: "<names>",
	ArgsLong: "<names> is a list of names identifying nodes to be created.",
}

var cmdNodeDelete = &cmdline.Command{
	Run:   runNodeDelete,
	Name:  "delete",
	Short: "Delete Jenkins slave nodes",
	Long: `
Delete Jenkins nodes. Uses the Jenkins REST API to delete existing slave nodes.
`,
	ArgsName: "<names>",
	ArgsLong: "<names> is a list of names identifying nodes to be deleted.",
}

const (
	jenkinsHost   = "http://veyron-jenkins:8001/jenkins"
	credentialsId = "73f76f53-8332-4259-bc08-d6f0b8521a5b"
)

var (
	// Global flags.
	flagColor   = flag.Bool("color", false, "Format output in color.")
	flagDryRun  = flag.Bool("n", false, "Show what commands will run, but do not execute them.")
	flagVerbose = flag.Bool("v", false, "Print verbose output.")
	// Command-specific flags.
	flagDescription string
	flagProject     string
	flagZone        string

	ipAddressRE = regexp.MustCompile(`^(\S*)\s*(\S*)\s(\S*)\s(\S*)\s(\S*)\s(\S*)$`)
)

func init() {
	cmdNodeCreate.Flags.StringVar(&flagDescription, "description", "", "Node description.")
	cmdNodeCreate.Flags.StringVar(&flagZone, "zone", "us-central1-f", "GCE zone of the machine.")
	cmdNodeCreate.Flags.StringVar(&flagProject, "project", "google.com:veyron", "GCE project of the machine.")
}

func newContext(cmd *cmdline.Command) *util.Context {
	return util.NewContextFromCommand(cmd, *flagColor, *flagDryRun, *flagVerbose)
}

// lookupIPAddress looks up the IP address for the given GCE node.
func lookupIPAddress(ctx *util.Context, node string) (string, error) {
	var out bytes.Buffer
	opts := ctx.Run().Opts()
	opts.Stdout = &out
	if err := ctx.Run().CommandWithOpts(opts, "gcloud", "compute", "instances",
		"--project", flagProject,
		"list", "--zones", flagZone, "-r", node); err != nil {
		return "", err
	}
	// The expected output is two lines, the first one is a header and
	// the second one is a node description.
	output := strings.TrimSpace(out.String())
	lines := strings.Split(output, "\n")
	if got, want := len(lines), 2; got != want {
		return "", fmt.Errorf("unexpected length of %v: got %v, want %v", lines, got, want)
	}
	// Parse the node information.
	matches := ipAddressRE.FindStringSubmatch(lines[1])
	if got, want := len(matches), 7; got != want {
		return "", fmt.Errorf("unexpected length of %v: got %v, want %v", matches, got, want)
	}
	// The external IP address is the fifth column.
	return matches[5], nil
}

// runNodeCreate adds slave node(s) to Jenkins configuration.
func runNodeCreate(cmd *cmdline.Command, args []string) error {
	ctx := newContext(cmd)
	jenkins, err := ctx.Jenkins(jenkinsHost)
	if err != nil {
		return err
	}

	for _, name := range args {
		ipAddress, err := lookupIPAddress(ctx, name)
		if err != nil {
			return err
		}
		fmt.Println(ipAddress)
		if err := jenkins.AddNodeToJenkins(name, ipAddress, flagDescription, credentialsId); err != nil {
			return err
		}
	}
	return nil
}

// runNodeDelete removes slave node(s) from Jenkins configuration.
func runNodeDelete(cmd *cmdline.Command, args []string) error {
	ctx := newContext(cmd)
	jenkins, err := ctx.Jenkins(jenkinsHost)
	if err != nil {
		return err
	}

	for _, node := range args {
		// Wait for the node to become idle.
		const numRetries = 60
		const retryPeriod = time.Minute
		for i := 0; i < numRetries; i++ {
			if ok, err := jenkins.IsNodeIdle(node); err != nil {
				return err
			} else if ok {
				break
			}
			time.Sleep(retryPeriod)
		}
		err := jenkins.RemoveNodeFromJenkins(node)
		if err != nil {
			return err
		}
	}
	return nil
}

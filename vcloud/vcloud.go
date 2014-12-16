// The following enables go generate to generate the doc.go file.
//go:generate go run $VEYRON_ROOT/veyron/go/src/veyron.io/lib/cmdline/testdata/gendoc.go .
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"veyron.io/lib/cmdline"
	"veyron.io/tools/lib/envutil"
	"veyron.io/tools/lib/runutil"
)

// TODO(toddw): Add tests by mocking out gcloud.

func main() {
	cmdVCloud.Main()
}

var cmdVCloud = &cmdline.Command{
	Name:  "vcloud",
	Short: "Veyron wrapper over gcloud, to manage GCE resources.",
	Long: `
The vcloud tool is a wrapper over the gcloud GCE resource management tool, to
simplify common usage scenarios.
`,
	Children: []*cmdline.Command{cmdList, cmdCP, cmdSH, cmdCopyAndRun},
}

var cmdList = &cmdline.Command{
	Run:   runList,
	Name:  "list",
	Short: "List GCE node information",
	Long: `
List GCE node information.  Runs 'gcloud compute instances list'.
`,
	ArgsName: "[nodes]",
	ArgsLong: "[nodes] " + nodesDesc + `
If [nodes] is not provided, lists information for all nodes.
`,
}

var cmdCP = &cmdline.Command{
	Run:   runCP,
	Name:  "cp",
	Short: "Copy files to/from GCE node(s)",
	Long: `
Copy files to GCE node(s).  Runs 'gcloud compute copy-files'.  The default is to
copy to/from all nodes in parallel.
`,
	ArgsName: "<nodes> <src...> <dst>",
	ArgsLong: "<nodes> " + nodesDesc + `
<src...> are the source file argument(s) to 'gcloud compute copy-files', and
<dst> is the destination.  The syntax for each file is:
  [:]file

Files with the ':' prefix are remote; files without any such prefix are local.

As with 'gcloud compute copy-files', if <dst> is local, all <src...> must be
remote.  If <dst> is remote, all <src...> must be local.

Each matching node in <nodes> is applied to the remote side of the copy
operation, either src or dst.  If <dst> is local and there is more than one
matching node, sub directories will be automatically created under <dst>.

E.g. if <nodes> matches A, B and C:
  // Copies local src{1,2,3} to {A,B,C}:dst
  vcloud cp src1 src2 src3 :dst
  // Copies remote {A,B,C}:src{1,2,3} to dst/{A,B,C} respectively.
  vcloud cp :src1 :src2 :src3 dst
`,
}

var cmdSH = &cmdline.Command{
	Run:   runSH,
	Name:  "sh",
	Short: "Start a shell or run a command on GCE node(s)",
	Long: `
Start a shell or run a command on GCE node(s).  Runs 'gcloud compute ssh'.
`,
	ArgsName: "<nodes> [command...]",
	ArgsLong: "<nodes> " + nodesDesc + `
[command...] is the shell command line to run on each node.  Specify the entire
command line without extra quoting, e.g. like this:
  vcloud sh jenkins-node uname -a
But NOT like this:
  vcloud sh jenkins-node 'uname -a'
If quoting and escaping becomes too complicated, use 'vcloud run' instead.

If <nodes> matches exactly one node and no [command] is given, sh starts a shell
on the specified node.

Otherwise [command...] is required; sh runs the command on all matching nodes.
The default is to run on all nodes in parallel.
`,
}

var cmdCopyAndRun = &cmdline.Command{
	Run:   runCopyAndRun,
	Name:  "run",
	Short: "Copy file(s) to GCE node(s) and run",
	Long: `
Copy file(s) to GCE node(s) and run.  Uses the logic of both cp and sh.
`,
	ArgsName: "<nodes> <files...> [++ [command...]]",
	ArgsLong: "<nodes> " + nodesDesc + `
<files...> are the local source file argument(s) to copy to each matching node.

[command...] is the shell command line to run on each node.  Specify the entire
command line without extra quoting, just like 'vcloud sh'.  If a command is
specified, it must be preceeded by a single ++ argument, to distinguish it from
the files.  If no command is given, runs the first file from <files...>.

We run the following logic on each matching node, in parallel by default:
  1) Create a temporary directory based on a random number.
  2) Copy all files into the temporary directory.
  3) Runs the [command...], or if no command is given, runs the first run file.
     All occurrences of the string literal '+TMPDIR' are replaced in the command
     with the temporary directory.  No replacement occurs for the run files,
     since the run files are all local.
  4) Delete the temporary directory.
`,
}

const (
	nodesDesc = `
is a comma-separated list of node name(s).  Each node name is a regular
expression, with matches performed on the full node name.  We select nodes that
match any of the regexps.  The comma-separated list allows you to easily specify
a list of specific node names, without using regexp alternation.  We assume node
names do not have embedded commas.
`
	parallelDesc = `
  <0   means all nodes in parallel
   0,1 means sequentially
   2+  means at most this many nodes in parallel
`
)

var (
	// Global flags.
	flagColor   = flag.Bool("color", false, "Format output in color.")
	flagDryRun  = flag.Bool("n", false, "Show what commands will run, but do not execute them.")
	flagVerbose = flag.Bool("v", false, "Print verbose output.")
	flagProject = flag.String("project", "google.com:veyron", "Specify the gcloud project.")
	flagUser    = flag.String("user", "veyron", "Run operations as the given user on each node.")
	// Command-specific flags.
	flagListNoHeader bool
	flagP            int
	flagFailFast     bool
)

func init() {
	cmdList.Flags.BoolVar(&flagListNoHeader, "noheader", false, "Don't print list table header.")
	cmdCP.Flags.IntVar(&flagP, "p", -1, "Copy to/from this many nodes in parallel."+parallelDesc)
	cmdSH.Flags.IntVar(&flagP, "p", -1, "Run command on this many nodes in parallel."+parallelDesc)
	cmdCopyAndRun.Flags.IntVar(&flagP, "p", -1, "Copy/run on this many nodes in parallel."+parallelDesc)
	cmdCP.Flags.BoolVar(&flagFailFast, "failfast", false, "Skip unstarted nodes after the first failing node.")
	cmdSH.Flags.BoolVar(&flagFailFast, "failfast", false, "Skip unstarted nodes after the first failing node.")
	cmdCopyAndRun.Flags.BoolVar(&flagFailFast, "failfast", false, "Skip unstarted nodes after the first failing node.")
}

// nodeInfo represents the node info returned by 'gcloud compute instances list'
type nodeInfo struct {
	Name        string
	Zone        string
	MachineType string
	InternalIP  string
	ExternalIP  string
	Status      string
}

// Parse parses the next line from r and fills in n.
func (n *nodeInfo) Parse(r io.Reader) error {
	_, err := fmt.Fscanln(r, &n.Name, &n.Zone, &n.MachineType, &n.InternalIP, &n.ExternalIP, &n.Status)
	return err
}

func (n nodeInfo) String() string {
	return fmt.Sprintf("%-18s %-15s %-15s %-15s %-15s %s", n.Name, n.Zone, n.MachineType, n.InternalIP, n.ExternalIP, n.Status)
}

// infoHeader contains the table headers from 'gcloud compute instances list'.
var infoHeader = nodeInfo{
	Name:        "NAME",
	Zone:        "ZONE",
	MachineType: "MACHINE_TYPE",
	InternalIP:  "INTERNAL_IP",
	ExternalIP:  "EXTERNAL_IP",
	Status:      "STATUS",
}

func addUser(s string) string {
	if *flagUser != "" {
		return *flagUser + "@" + s
	}
	return s
}

// StartShell starts a shell on node n.
func (n nodeInfo) StartShell(run *runutil.Run) error {
	return run.Command("gcloud", "compute", "ssh", addUser(n.Name), "--project", *flagProject, "--zone", n.Zone)
}

// RunCopy runs the copy from srcs to dst on node x.  Assumes we've already
// validated that either dst is remote and all srcs are local, or vice versa.
func (n nodeInfo) RunCopy(run *runutil.Run, srcs []string, dst string, makeSubdir bool) runResult {
	if strings.HasPrefix(dst, ":") {
		dst = addUser(n.Name + dst)
	} else {
		copysrcs := make([]string, len(srcs))
		for i, src := range srcs {
			copysrcs[i] = addUser(n.Name + src)
		}
		srcs = copysrcs
		if makeSubdir {
			// We're copying into a local dst, and we have more than one copy running,
			// so we need to make subdirs to keep each copy separate.
			dst = path.Join(dst, n.Name)
			if err := os.Mkdir(dst, os.ModePerm); err != nil {
				return runResult{node: n, err: err}
			}
		}
	}
	args := []string{"compute", "copy-files"}
	args = append(args, srcs...)
	args = append(args, dst)
	args = append(args, "--project", *flagProject, "--zone", n.Zone)
	var stdouterr bytes.Buffer
	opts := run.Opts()
	opts.Stdin = nil
	opts.Stdout = &stdouterr
	opts.Stderr = &stdouterr
	err := run.CommandWithOpts(opts, "gcloud", args...)
	return runResult{node: n, out: stdouterr.String(), err: err}
}

// RunCommand runs cmdline on node n.
func (n nodeInfo) RunCommand(run *runutil.Run, cmdline []string) runResult {
	var stdouterr bytes.Buffer
	opts := run.Opts()
	opts.Stdin = nil
	opts.Stdout = &stdouterr
	opts.Stderr = &stdouterr
	err := run.CommandWithOpts(opts, "gcloud", "compute", "ssh", addUser(n.Name), "--project", *flagProject, "--zone", n.Zone, "--command", quoteForCommand(cmdline))
	return runResult{node: n, out: stdouterr.String(), err: err}
}

func quoteForCommand(cmdline []string) string {
	// This is probably wrong, but it works for simple cases.  This is very
	// complicated because there are multiple levels of escaping, from the input
	// shell, runutil.Run, gcloud, the node itself, etc.
	//
	// For more complicated scripts, use 'vcloud run'.
	ret := ""
	for i, arg := range cmdline {
		if strings.ContainsAny(arg, " ") {
			arg = `"` + arg + `"`
		}
		if i > 0 {
			ret += " "
		}
		ret += arg
	}
	return ret
}

// runResult describes the result of running a command on a node.
type runResult struct {
	node    nodeInfo
	out     string
	err     error
	skipped bool
}

// Merge merges the results from r2 into r.
func (r *runResult) Merge(r2 runResult, format string, v ...interface{}) {
	msg := fmt.Sprintf(format, v...)
	if r2.err != nil {
		if r.err != nil {
			// Output r.err first so we don't lose it.
			r.out += fmt.Sprintf("%s FAIL: %v\n", msg, r.err)
		}
		r.err = r2.err
	}
	r.out += msg + "\n"
	r.out += r2.out
}

func (r runResult) String() string {
	var ret string
	if r.out != "" {
		ret += prefixLines(r.node.Name+": ", r.out) + "\n"
	}
	switch {
	case r.skipped:
		ret += fmt.Sprintf("%s SKIP\n", r.node.Name)
	case r.err != nil:
		ret += fmt.Sprintf("%s FAIL: %v\n", r.node.Name, r.err)
	default:
		ret += fmt.Sprintf("%s DONE\n", r.node.Name)
	}
	return ret
}

// prefixLines adds pre to each newline-terminated line in lines.
func prefixLines(pre, lines string) string {
	newpre := "\n" + pre
	return strings.TrimSuffix(pre+strings.Replace(lines, "\n", newpre, -1), newpre)
}

// nodeInfos holds a slice of nodeInfo representing multiple nodes, and supports
// convenient methods to manipulate and run commands on the nodes.
type nodeInfos []nodeInfo

// run runs fn on each of the nodes in x, obeying flagP and flagFailFast.
func (x nodeInfos) run(w io.Writer, fn func(node nodeInfo) runResult) error {
	parallel := flagP
	switch {
	case flagP == 0:
		parallel = 1
	case flagP < 0:
		parallel = len(x)
	}
	failFast := make(chan bool)
	semaphore := make(chan bool, parallel)
	results := make(chan runResult, len(x))
	// Only spawn a maximum of parallel goroutines at a time, controlled by the
	// semaphore.  Each goroutine runs fn and sends the results back on results.
	// We spawn an outer goroutine so that we can output results as they're
	// available from any workers.
	go func() {
		for i, node := range x {
			select {
			case semaphore <- true:
				go func(n nodeInfo) {
					results <- fn(n)
					<-semaphore
				}(node)
			case <-failFast:
				// Skip all remaining nodes once we get the failFast signal.
				for j := i; j < len(x); j++ {
					results <- runResult{x[j], "", nil, true}
				}
				return
			}
		}
	}()
	// Collect results; each node returns a result even if its skipped.
	var skip, fail, done nodeInfos
	for ix := 0; ix < len(x); ix++ {
		result := <-results
		fmt.Fprint(w, result)
		switch {
		case result.skipped:
			skip = append(skip, result.node)
		case result.err != nil:
			fail = append(fail, result.node)
			if flagFailFast && len(fail) == 1 {
				close(failFast)
			}
		default:
			done = append(done, result.node)
		}
	}
	if len(fail) > 0 {
		var msg string
		if len(done) > 0 {
			msg += fmt.Sprintf("\nDONE %d/%d nodes: %v", len(done), len(x), done.Names())
		}
		if len(skip) > 0 {
			msg += fmt.Sprintf("\nSKIP %d/%d nodes: %v", len(skip), len(x), skip.Names())
		}
		msg += fmt.Sprintf("\nFAIL %d/%d nodes: %v", len(fail), len(x), fail.Names())
		return errors.New(msg)
	}
	fmt.Fprintf(w, "\nDONE %d nodes: %v\n", len(done), done.Names())
	return nil
}

// RunCopy runs the copy from srcs to dst on all nodes in x.
func (x nodeInfos) RunCopy(run *runutil.Run, srcs []string, dst string, makeSubdir bool) error {
	fn := func(node nodeInfo) runResult { return node.RunCopy(run, srcs, dst, makeSubdir) }
	return x.run(run.Opts().Stdout, fn)
}

// RunCommand runs the cmdline on all nodes in x.
func (x nodeInfos) RunCommand(run *runutil.Run, cmdline []string) error {
	fn := func(node nodeInfo) runResult { return node.RunCommand(run, cmdline) }
	return x.run(run.Opts().Stdout, fn)
}

// RunCopyAndRun implements the 'vcloud run' command.
func (x nodeInfos) RunCopyAndRun(run *runutil.Run, files, cmdline []string) error {
	// 0) Pick a random number so that we use the same tmpdir on each node.
	rand.Seed(time.Now().UnixNano())
	tmpdir := fmt.Sprintf("./tmp_%X", rand.Int63())
	fn := func(node nodeInfo) runResult {
		cmdline := cmdline
		result := runResult{node: node}
		// 1) Create temporary directory.
		result.Merge(node.RunCommand(run, []string{"mkdir", tmpdir}), "[run] create tmpdir %q", tmpdir)
		if result.err != nil {
			return result
		}
		// 2) Copy all files into the temporary directory.
		result.Merge(node.RunCopy(run, files, ":"+tmpdir, false), "[run] copy files %v", files)
		if result.err == nil {
			// 3) Runs the cmdline (only if copy successful), expanding +TMPDIR.
			if len(cmdline) == 0 {
				// Run the first file if no commands are specified.
				cmdline = []string{tmpdir + "/" + filepath.Base(files[0])}
			} else {
				copycmds := make([]string, len(cmdline))
				for i, cmd := range cmdline {
					copycmds[i] = strings.Replace(cmd, "+TMPDIR", tmpdir, -1)
				}
				cmdline = copycmds
			}
			result.Merge(node.RunCommand(run, cmdline), "[run] run cmdline %v", cmdline)
		}
		// 4) Delete the temporary directory (always, if created successfully).
		result.Merge(node.RunCommand(run, []string{"rm", "-rf", tmpdir}), "[run] delete tmpdir %q", tmpdir)
		return result
	}
	return x.run(run.Opts().Stdout, fn)
}

func (x nodeInfos) String() string {
	var ret string
	if !flagListNoHeader {
		ret += infoHeader.String() + "\n"
	}
	for _, node := range x {
		ret += node.String() + "\n"
	}
	return ret
}

func (x nodeInfos) Sort()              { sort.Sort(x) }
func (x nodeInfos) Len() int           { return len(x) }
func (x nodeInfos) Less(i, j int) bool { return x[i].Name < x[j].Name }
func (x nodeInfos) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }

func (x nodeInfos) Names() []string {
	var ret []string
	for _, node := range x {
		ret = append(ret, node.Name)
	}
	return ret
}

// MatchNames returns all nodes that match exprlist, which is a comma-separated
// list of regexps.
func (x nodeInfos) MatchNames(exprlist string) (nodeInfos, error) {
	relist, err := parseRegexpList(exprlist)
	if err != nil {
		return nil, err
	}
	var ret nodeInfos
	for _, node := range x {
		if relist.AnyMatch(node.Name) {
			ret = append(ret, node)
		}
	}
	if len(ret) == 0 {
		return nil, fmt.Errorf("%#q doesn't match any node names", exprlist)
	}
	return ret, nil
}

// regexpList holds a list of regular expressions.
type regexpList []*regexp.Regexp

// parseRegexpList parses a comma-separated list of regular expressions.
func parseRegexpList(exprlist string) (regexpList, error) {
	var ret regexpList
	for _, expr := range strings.Split(exprlist, ",") {
		expr = strings.TrimSpace(expr)
		if expr == "" {
			continue
		}
		// Make sure the regexp performs a full match against the target string.
		if !strings.HasPrefix(expr, "^") {
			expr = "^" + expr
		}
		if !strings.HasSuffix(expr, "$") {
			expr = expr + "$"
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		ret = append(ret, re)
	}
	return ret, nil
}

// AnyMatch returns true iff any regexp in x matches s.
func (x regexpList) AnyMatch(s string) bool {
	for _, re := range x {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// listAll runs 'gcloud compute instances list' to list all nodes, and parses
// the results into nodeInfos.  If dryrun is set, only prints the command.
func listAll(run *runutil.Run, dryrun bool) (nodeInfos, error) {
	var stdout bytes.Buffer
	opts := run.Opts()
	opts.DryRun = dryrun
	opts.Stdin = nil
	opts.Stdout = &stdout
	if err := run.CommandWithOpts(opts, "gcloud", "compute", "instances", "list"); err != nil {
		return nil, err
	}
	if run.Opts().Verbose {
		fmt.Fprintln(run.Opts().Stdout, stdout.String())
	}
	// The first line is the column headings.
	var head nodeInfo
	if err := head.Parse(&stdout); err != nil {
		return nil, err
	}
	if got, want := head, infoHeader; got != want {
		// A mismatch indicates the columns may have been re-organized, and the rest
		// of our logic operating on each nodeInfo may be incorrect.
		return nil, fmt.Errorf("got list headers %v, want %v\n", got, want)
	}
	// Parse each line until we get to EOF.
	var all nodeInfos
ParseLineLoop:
	for {
		var node nodeInfo
		switch err := node.Parse(&stdout); {
		case err == io.EOF:
			break ParseLineLoop
		case err != nil:
			return nil, err
		}
		all = append(all, node)
	}
	all.Sort()
	return all, nil
}

// listMatching runs listAll and matches the resulting nodes against exprlist, a
// comma-separated list of regular expressions.
func listMatching(run *runutil.Run, exprlist string) (nodeInfos, error) {
	all, err := listAll(run, false) // Never dry-run, even if -n is set.
	if err != nil {
		return nil, err
	}
	match, err := all.MatchNames(exprlist)
	if err != nil {
		return nil, err
	}
	return match, nil
}

func newRun(cmd *cmdline.Command) *runutil.Run {
	env := envutil.ToMap(os.Environ())
	return runutil.New(env, os.Stdin, cmd.Stdout(), cmd.Stderr(), *flagColor, *flagDryRun, *flagVerbose)
}

func runList(cmd *cmdline.Command, args []string) error {
	all, err := listAll(newRun(cmd), *flagDryRun)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		fmt.Fprint(cmd.Stdout(), all)
		return nil
	case len(args) == 1:
		matches, err := all.MatchNames(args[0])
		if err != nil {
			return cmd.UsageErrorf("%v", err)
		}
		fmt.Fprint(cmd.Stdout(), matches)
		return nil
	}
	return cmd.UsageErrorf("too many args")
}

func runCP(cmd *cmdline.Command, args []string) error {
	if len(args) < 3 {
		return cmd.UsageErrorf("need at least three args")
	}
	run := newRun(cmd)
	nodes, err := listMatching(run, args[0])
	if err != nil {
		return cmd.UsageErrorf("%v", err)
	}
	// If dst is remote, all srcs must be local.  If dst is local, all srcs must
	// be remote.
	makeSubdir := false
	dstIndex := len(args) - 1
	srcs, dst := args[1:dstIndex], args[dstIndex]
	if strings.HasPrefix(dst, ":") {
		for _, src := range srcs {
			if strings.HasPrefix(src, ":") {
				return cmd.UsageErrorf("dst is remote; all srcs must be local")
			}
		}
	} else {
		for _, src := range srcs {
			if !strings.HasPrefix(src, ":") {
				return cmd.UsageErrorf("dst is local; all srcs must be remote")
			}
		}
		// If we have more than one matching node, and dst is local and srcs are
		// remote, it's be pointless to copy into the same dst dir; the remote
		// copies will overwrite each other.
		if len(nodes) > 1 {
			makeSubdir = true
		}
	}
	return nodes.RunCopy(run, srcs, dst, makeSubdir)
}

func runSH(cmd *cmdline.Command, args []string) error {
	if len(args) == 0 {
		return cmd.UsageErrorf("no node(s) specified")
	}
	run := newRun(cmd)
	nodes, err := listMatching(run, args[0])
	if err != nil {
		return cmd.UsageErrorf("%v", err)
	}
	if len(nodes) == 1 && len(args) == 1 {
		return nodes[0].StartShell(run)
	}
	if len(args) == 1 {
		return cmd.UsageErrorf("must specify command; more than one matching node: %v", nodes.Names())
	}
	return nodes.RunCommand(run, args[1:])
}

func runCopyAndRun(cmd *cmdline.Command, args []string) error {
	if len(args) < 2 {
		return cmd.UsageErrorf("need at least two args")
	}
	files, cmdline, err := splitCopyAndRunArgs(args[1:])
	if err != nil {
		return cmd.UsageErrorf("%v", err)
	}
	run := newRun(cmd)
	nodes, err := listMatching(run, args[0])
	if err != nil {
		return cmd.UsageErrorf("%v", err)
	}
	return nodes.RunCopyAndRun(run, files, cmdline)
}

func splitCopyAndRunArgs(args []string) (files, cmdline []string, _ error) {
SplitArgsLoop:
	for i, arg := range args {
		switch {
		case arg == "":
			continue
		case arg == "++":
			// Everything after this is the cmdline.
			cmdline = args[i+1:]
			break SplitArgsLoop
		default:
			if strings.HasPrefix(arg, ":") {
				return nil, nil, fmt.Errorf("all run files must be local")
			}
			files = append(files, arg)
		}
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no run files in %v", args)
	}
	return
}
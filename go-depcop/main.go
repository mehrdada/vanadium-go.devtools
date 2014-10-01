// Below is the output from $(go-depcop help -style=godoc ...)

/*
The go-depcop tool checks if a package imports respects outgoing and
incoming dependency constraints described in the GO.PACKAGE files.

go-depcop also enforces "internal" package rules.

GO.PACKAGE files are traversed hierarchically, from the deepmost
package to GOROOT, until a matching rule is found.  If no matching
rule is found, the default behavior is to allow the dependency,
to stay compatible with existing packages that do not include
dependency rules.

GO.PACKAGE is a JSON file with a structure along the lines of:
   {
     "dependencies": {
       "outgoing": [
         {"allow": "allowpattern1/..."},
         {"deny": "denypattern"},
         {"allow": "pattern2"}
       ],
       "incoming": [
         {"allow": "pattern3"},
         {"deny": "pattern4"}
       ]
     }
   }

Usage:
   go-depcop [flags] <command>

The go-depcop commands are:
   check       Check package dependency constraints
   list        List outgoing package dependencies
   rlist       List incoming package dependencies
   selfupdate  Update the go-depcop tool
   version     Print version
   help        Display help for commands

The go-depcop flags are:
   -v=false: Print verbose output.

Go-Depcop Check

Check package dependency constraints.

Usage:
   go-depcop check [flags] <packages>

<packages> is a list of packages

The check flags are:
   -r=false: Check dependencies recursively.

Go-Depcop List

List outgoing package dependencies.

Usage:
   go-depcop list [flags] <packages>

<packages> is a list of packages

The list flags are:
   -pretty-print=false: Make output easy to read, indenting nested dependencies.
   -show-goroot=false: Show packages in goroot.
   -transitive=false: List transitive dependencies.

Go-Depcop Rlist

List incoming package dependencies.

Usage:
   go-depcop rlist <packages>

<packages> is a list of packages

Go-Depcop Selfupdate

Download and install the latest version of the go-depcop tool.

Usage:
   go-depcop selfupdate [flags]

The selfupdate flags are:
   -manifest=absolute: Name of the project manifest.

Go-Depcop Version

Print version of the go-depcop tool.

Usage:
   go-depcop version

Go-Depcop Help

Help displays usage descriptions for this command, or usage descriptions for
sub-commands.

Usage:
   go-depcop help [flags] [command ...]

[command ...] is an optional sequence of commands to display detailed usage.
The special-case "help ..." recursively displays help for this command and all
sub-commands.

The help flags are:
   -style=text: The formatting style for help output, either "text" or "godoc".
*/
package main

import (
	"tools/go-depcop/impl"
)

func main() {
	impl.Root().Main()
}

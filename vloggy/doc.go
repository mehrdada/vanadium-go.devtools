// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file was auto-generated via go generate.
// DO NOT UPDATE MANUALLY

/*
Command vloggy checks for and injects logging statements into Go source code.

When checking, it ensures that all implementations in <packages> of all exported
interfaces declared in packages passed to the -interface flag have an
appropriate logging construct.

When injecting, it modifies the source code to inject such logging constructs.

LIMITATIONS:

vloggy requires the "v.io/x/lib/vlog" to be imported as "vlog".  Aliasing the
log package to another name makes vloggy ignore the calls.  Importing any other
package with the name "vlog" will invoke undefined behavior.

Usage:
   vloggy [flags] <command>

The vloggy commands are:
   check       Check for log statements in public API implementations
   inject      Inject log statements in public API implementations
   version     Print version
   help        Display help for commands or topics
Run "vloggy help [command]" for command usage.

The vloggy flags are:
 -color=true
   Use color to format output.
 -n=false
   Show what commands will run but do not execute them.
 -progress=false
   Print verbose progress information.
 -v=false
   Print verbose output.

Vloggy Check

Check for log statements in public API implementations.

Usage:
   vloggy check [flags] <packages>

<packages> is the list of packages to be checked.

The vloggy check flags are:
 -interface=
   Comma-separated list of interface packages (required)

Vloggy Inject

Inject log statements in public API implementations. Note that inject modifies
<packages> in-place.  It is a good idea to commit changes to version control
before running this tool so you can see the diff or revert the changes.

Usage:
   vloggy inject [flags] <packages>

<packages> is the list of packages to inject log statements in.

The vloggy inject flags are:
 -gofmt=true
   Automatically run gofmt on the modified files
 -interface=
   Comma-separated list of interface packages (required)

Vloggy Version

Print version of the vloggy tool.

Usage:
   vloggy version

Vloggy Help

Help with no args displays the usage of the parent command.

Help with args displays the usage of the specified sub-command or help topic.

"help ..." recursively displays help for all commands and topics.

The output is formatted to a target width in runes.  The target width is
determined by checking the environment variable CMDLINE_WIDTH, falling back on
the terminal width from the OS, falling back on 80 chars.  By setting
CMDLINE_WIDTH=x, if x > 0 the width is x, if x < 0 the width is unlimited, and
if x == 0 or is unset one of the fallbacks is used.

Usage:
   vloggy help [flags] [command/topic ...]

[command/topic ...] optionally identifies a specific sub-command or help topic.

The vloggy help flags are:
 -style=default
   The formatting style for help output, either "default" or "godoc".
*/
package main

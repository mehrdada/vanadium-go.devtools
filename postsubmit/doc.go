// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file was auto-generated via go generate.
// DO NOT UPDATE MANUALLY

/*
Command postsubmit performs Vanadium postsubmit related functions.

Usage:
   postsubmit [flags] <command>

The postsubmit commands are:
   poll        Poll changes and start corresponding builds on Jenkins
   help        Display help for commands or topics

The postsubmit flags are:
 -color=true
   Use color to format output.
 -host=
   The Jenkins host. Presubmit will not send any CLs to an empty host.
 -v=false
   Print verbose output.

The global flags are:
 -metadata=<just specify -metadata to activate>
   Displays metadata for the program and exits.
 -time=false
   Dump timing information to stderr before exiting the program.

Postsubmit poll - Poll changes and start corresponding builds on Jenkins

Poll changes and start corresponding builds on Jenkins.

Usage:
   postsubmit poll [flags]

The postsubmit poll flags are:
 -manifest=
   Name of the project manifest.

 -color=true
   Use color to format output.
 -host=
   The Jenkins host. Presubmit will not send any CLs to an empty host.
 -v=false
   Print verbose output.

Postsubmit help - Display help for commands or topics

Help with no args displays the usage of the parent command.

Help with args displays the usage of the specified sub-command or help topic.

"help ..." recursively displays help for all commands and topics.

Usage:
   postsubmit help [flags] [command/topic ...]

[command/topic ...] optionally identifies a specific sub-command or help topic.

The postsubmit help flags are:
 -style=compact
   The formatting style for help output:
      compact   - Good for compact cmdline output.
      full      - Good for cmdline output, shows all global flags.
      godoc     - Good for godoc processing.
      shortonly - Only output short description.
   Override the default by setting the CMDLINE_STYLE environment variable.
 -width=<terminal width>
   Format output to this target width in runes, or unlimited if width < 0.
   Defaults to the terminal width if available.  Override the default by setting
   the CMDLINE_WIDTH environment variable.
*/
package main

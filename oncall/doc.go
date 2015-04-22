// Copyright 2015 The Vanadium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file was auto-generated via go generate.
// DO NOT UPDATE MANUALLY

/*
Command oncall implements oncall specific utilities used by Vanadium team.

Usage:
   oncall [flags] <command>

The oncall commands are:
   collect     Collect data for oncall dashboard
   serve       Serve oncall dashboard data from Google Storage
   help        Display help for commands or topics

The oncall flags are:
 -color=true
   Use color to format output.
 -n=false
   Show what commands will run, but do not execute them.
 -v=false
   Print verbose output.

Oncall collect

This subcommand collects data from Google Cloud Monitoring and stores the
processed data to Google Storage.

Usage:
   oncall collect [flags]

The oncall collect flags are:
 -account=
   The service account used to communicate with GCM.
 -key=
   The path to the service account's key file.
 -project=
   The GCM's corresponding GCE project ID.

Oncall serve

Serve oncall dashboard data from Google Storage.

Usage:
   oncall serve [flags]

The oncall serve flags are:
 -cache=
   Directory to use for caching files.
 -port=8000
   Port for the server.
 -static=
   Directory to use for serving static files.

Oncall help

Help with no args displays the usage of the parent command.

Help with args displays the usage of the specified sub-command or help topic.

"help ..." recursively displays help for all commands and topics.

Output is formatted to a target width in runes, determined by checking the
CMDLINE_WIDTH environment variable, falling back on the terminal width, falling
back on 80 chars.  By setting CMDLINE_WIDTH=x, if x > 0 the width is x, if x < 0
the width is unlimited, and if x == 0 or is unset one of the fallbacks is used.

Usage:
   oncall help [flags] [command/topic ...]

[command/topic ...] optionally identifies a specific sub-command or help topic.

The oncall help flags are:
 -style=compact
   The formatting style for help output:
      compact - Good for compact cmdline output.
      full    - Good for cmdline output, shows all global flags.
      godoc   - Good for godoc processing.
   Override the default by setting the CMDLINE_STYLE environment variable.
*/
package main

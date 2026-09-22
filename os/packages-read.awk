# os/packages-read.awk -- the ONE reader of os/packages.list.
#
# os/install.sh and scripts/ci-lint.sh both use this file. Two readers of one
# grammar drift apart: the first pair disagreed about a line with two
# architecture tags, and such a line then installed on NEITHER architecture with
# nothing to show it. tests/ci/packages-grammar.sh proves the reader against a
# file of sample lines.
#
# THE GRAMMAR
#   A "#" starts a rationale comment. The rest of the line goes away.
#   A blank line is nothing.
#   Tags come first and each one starts with "@". A line can carry several.
#   @image        the package belongs to an image build only.
#   @<arch>       the package belongs to that architecture.
#   Several architecture tags are an OR SET: "@x86_64 @aarch64 pkg" means both.
#   No architecture tag at all means every architecture.
#   Every word after the tags is a package name. A line may name more than one.
#
# VARIABLES
#   arch   the architecture we install for, for example "x86_64"
#   onbox  1 = the running system, 0 = an image tree
#   want   "keep" prints the packages to install.
#          "skip" prints the @image packages that on-box mode leaves out.
#          "check" prints one line per input line: "OK <n> <names>" or
#                  "BAD <reason>", which is what the lint and the test read.

{ line = $0 }
{ sub(/[ \t]*#.*$/, "") }        # drop the rationale comment
{ gsub(/^[ \t]+|[ \t]+$/, "") }  # trim
$0 == "" { next }

{
	# Read the tags. "archtags" counts how many architecture tags the line has,
	# and "archhit" says if one of them is ours. With no architecture tag the line
	# is for every architecture.
	n = 1
	image = 0
	archtags = 0
	archhit = 0
	bad = ""
	while (n <= NF && substr($n, 1, 1) == "@") {
		if ($n == "@image") {
			image = 1
		} else if ($n == "@x86_64" || $n == "@aarch64") {
			archtags++
			if ($n == "@" arch) archhit = 1
		} else {
			bad = "unknown tag " $n
		}
		n++
	}
	if (n > NF && bad == "") bad = "the line has tags and no package name"

	if (want == "check") {
		if (bad != "") {
			printf "BAD %s\n", bad
		} else {
			printf "OK %d", NF - n + 1
			for (i = n; i <= NF; i++) printf " %s", $i
			printf "\n"
		}
		next
	}
	if (bad != "") next
	if (archtags > 0 && archhit == 0) next
	if (image && onbox == 1) {
		if (want == "skip") for (i = n; i <= NF; i++) print $i
		next
	}
	if (want == "keep") for (i = n; i <= NF; i++) print $i
}

# Blastdoor's built-in backstop: the door is closed unless a policy opens it.
#
# Every resource change in the plan scores the maximum unless some policy has
# explicitly claimed it by adding its address to `classified`. Loaded by
# default; disable with `--no-base-policy` if you want to score only what your
# own rules match.
package blastdoor

# Declares `classified` so this file compiles on its own, when no policy of
# yours defines it yet. Iterating an empty array contributes nothing, leaving
# an empty set — so with no policies loaded, everything is unclassified.
classified contains addr if {
	some addr in []
}

deny contains {"msg": msg, "score": 100, "resource": rc.address} if {
	some rc in input.resource_changes
	not no_op(rc)
	not classified[rc.address]
	msg := sprintf("%s: no policy classifies %s — scored as maximum risk by default", [rc.address, rc.type])
}

no_op(rc) if {
	rc.change.actions == ["no-op"]
}

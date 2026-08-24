# Allowing, in its simplest form.
#
# Reading a data source changes no infrastructure, but blastdoor cannot know
# that — a change no rule matches is denied. Most real modules read something
# (Vault, remote state, an AMI lookup), so without a rule like this every plan
# is denied and the gate stops meaning anything.
package blastdoor

allow contains {"resource": rc.address, "reason": "reading a data source changes no infrastructure"} if {
	some rc in input.resource_changes
	data_read(rc)
}

# Deliberately narrow: only a data source doing nothing but a read. A managed
# resource is never matched here, whatever its action.
data_read(rc) if {
	rc.mode == "data"
	rc.change.actions == ["read"]
}

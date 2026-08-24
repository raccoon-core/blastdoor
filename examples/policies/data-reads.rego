# Green-flagging, in its simplest form.
#
# Reading a data source changes no infrastructure, but the backstop cannot
# know that — it scores anything unclaimed at 100. Most real modules read
# something (Vault, remote state, an AMI lookup), so without a rule like this
# every plan needs review and the gate stops meaning anything.
#
# This is the shape of every allow: score it 0, then claim it.
package blastdoor

deny contains {"msg": msg, "score": 0, "resource": rc.address} if {
	some rc in input.resource_changes
	data_read(rc)
	msg := sprintf("%s: reading a data source, no infrastructure changes", [rc.address])
}

classified contains rc.address if {
	some rc in input.resource_changes
	data_read(rc)
}

# Deliberately narrow: only a data source doing nothing but a read. A managed
# resource is never matched here, and neither is a data block that Terraform
# plans to do anything else with.
data_read(rc) if {
	rc.mode == "data"
	rc.change.actions == ["read"]
}

# A worked example: scoring Kafka topic, ACL and user changes.
#
# Read this alongside examples/README.md. The pattern is the same for any
# provider — match a resource change, emit a score, and claim the address in
# `classified` so the built-in backstop stops scoring it at 100.
package blastdoor

# --- topics ---------------------------------------------------------------

# Creating a topic is additive and cheap to undo.
deny contains {"msg": msg, "score": 0, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
	msg := sprintf("%s: creating topic %s", [rc.address, rc.change.after.name])
}

# Deleting a topic destroys its data, whether outright or as the delete half
# of a replace.
deny contains {"msg": msg, "score": 80, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	destroys(rc)
	msg := sprintf("%s: deleting a topic destroys its data", [rc.address])
}

# Shrinking a topic loses partitions or redundancy.
deny contains {"msg": msg, "score": 90, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["update"]
	shrinks(rc)
	msg := sprintf("%s: reducing partitions or replication_factor", [rc.address])
}

# Any other in-place edit is a config tweak: retention, cleanup policy, and
# the like.
deny contains {"msg": msg, "score": 10, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["update"]
	not shrinks(rc)
	msg := sprintf("%s: topic configuration change", [rc.address])
}

shrinks(rc) if {
	rc.change.after.partitions < rc.change.before.partitions
}

shrinks(rc) if {
	rc.change.after.replication_factor < rc.change.before.replication_factor
}

# --- ACLs -----------------------------------------------------------------

# A wildcard Allow grant hands out far more than it looks like it does.
deny contains {"msg": msg, "score": 90, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	rc.change.actions == ["create"]
	rc.change.after.acl_permission_type == "Allow"
	wildcard(rc)
	msg := sprintf("%s: Allow ACL with a wildcard principal or resource", [rc.address])
}

# A scoped Allow grant still widens access, but only where it says.
deny contains {"msg": msg, "score": 20, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	rc.change.actions == ["create"]
	rc.change.after.acl_permission_type == "Allow"
	not wildcard(rc)
	msg := sprintf("%s: scoped Allow ACL for %s", [rc.address, rc.change.after.acl_principal])
}

# A Deny ACL only ever narrows access.
deny contains {"msg": msg, "score": 0, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	rc.change.actions == ["create"]
	rc.change.after.acl_permission_type == "Deny"
	msg := sprintf("%s: Deny ACL", [rc.address])
}

wildcard(rc) if {
	contains(rc.change.after.acl_principal, "*")
}

wildcard(rc) if {
	contains(rc.change.after.resource_name, "*")
}

# --- users ----------------------------------------------------------------

deny contains {"msg": msg, "score": 10, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_user_scram_credential"
	rc.change.actions == ["create"]
	msg := sprintf("%s: creating a user", [rc.address])
}

deny contains {"msg": msg, "score": 60, "resource": rc.address} if {
	some rc in input.resource_changes
	rc.type == "kafka_user_scram_credential"
	destroys(rc)
	msg := sprintf("%s: deleting a user breaks whatever authenticates as it", [rc.address])
}

# --- shared helpers -------------------------------------------------------

destroys(rc) if {
	some action in rc.change.actions
	action == "delete"
}

# Claim every change these rules understand. Anything not claimed here is
# scored 100 by blastdoor's built-in backstop — including a Kafka change in a
# shape none of the rules above match, such as a replace.
classified contains rc.address if {
	some rc in input.resource_changes
	rc.type in {"kafka_topic", "kafka_acl", "kafka_user_scram_credential"}
	scored_shape(rc)
}

scored_shape(rc) if {
	rc.change.actions == ["create"]
}

scored_shape(rc) if {
	rc.change.actions == ["update"]
}

scored_shape(rc) if {
	rc.change.actions == ["delete"]
}

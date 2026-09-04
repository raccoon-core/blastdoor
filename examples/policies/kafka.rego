# A worked example: judging Kafka topic, ACL and user changes.
#
# Every rule answers one question about one change — is this fine, does a
# person need to look, or is it not allowed? Add the change to `allow`,
# `review` or `deny` with a reason. A change no rule matches is sent to
# review, so there is nothing extra to write to make that happen.
package blastdoor

# --- topics ---------------------------------------------------------------

# Creating a topic is additive and easy to undo.
allow contains {"resource": rc.address, "reason": sprintf("creating topic %s", [rc.change.after.name])} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}

# Deleting a topic destroys its data. Recoverable, but somebody should say so
# on purpose — whether outright or as the delete half of a replace.
review contains {"resource": rc.address, "reason": "deleting a topic destroys its data"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	destroys(rc)
}

# Shrinking a topic is not a thing Kafka can do to a live topic, and asking
# for it loses partitions or redundancy. No approval makes that work.
deny contains {"resource": rc.address, "reason": "reducing partitions or replication_factor is not a supported operation"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["update"]
	shrinks(rc)
}

# Any other in-place edit is a config tweak: retention, cleanup policy.
allow contains {"resource": rc.address, "reason": "topic configuration change"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["update"]
	not shrinks(rc)
}

shrinks(rc) if {
	rc.change.after.partitions < rc.change.before.partitions
}

shrinks(rc) if {
	rc.change.after.replication_factor < rc.change.before.replication_factor
}

# --- ACLs -----------------------------------------------------------------

# A wildcard Allow grant hands out far more than it appears to, and there is
# no version of it this policy is willing to accept.
deny contains {"resource": rc.address, "reason": "Allow ACL with a wildcard principal or resource grants unbounded access"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	rc.change.actions == ["create"]
	rc.change.after.acl_permission_type == "Allow"
	wildcard(rc)
}

# A scoped Allow grant still widens access, so a person signs it off.
review contains {"resource": rc.address, "reason": sprintf("grants %s access to %s", [rc.change.after.acl_principal, rc.change.after.resource_name])} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	rc.change.actions == ["create"]
	rc.change.after.acl_permission_type == "Allow"
	not wildcard(rc)
}

# A Deny ACL only ever narrows access.
allow contains {"resource": rc.address, "reason": "Deny ACL only restricts access"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	rc.change.actions == ["create"]
	rc.change.after.acl_permission_type == "Deny"
}

wildcard(rc) if {
	contains(rc.change.after.acl_principal, "*")
}

wildcard(rc) if {
	contains(rc.change.after.resource_name, "*")
}

# --- users ----------------------------------------------------------------

allow contains {"resource": rc.address, "reason": "creating a user"} if {
	some rc in input.resource_changes
	rc.type == "kafka_user_scram_credential"
	rc.change.actions == ["create"]
}

review contains {"resource": rc.address, "reason": "deleting a user breaks whatever authenticates as it"} if {
	some rc in input.resource_changes
	rc.type == "kafka_user_scram_credential"
	destroys(rc)
}

# --- shared ---------------------------------------------------------------

destroys(rc) if {
	some action in rc.change.actions
	action == "delete"
}

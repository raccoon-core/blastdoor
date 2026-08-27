# Your first policy

A policy is Rego in `package blastdoor` that puts changes into one of three
rule sets, each with a reason:

```rego
package blastdoor

allow contains {"resource": rc.address, "reason": "creating a topic is additive"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["create"]
}

review contains {"resource": rc.address, "reason": "deleting a topic destroys its data"} if {
	some rc in input.resource_changes
	rc.type == "kafka_topic"
	rc.change.actions == ["delete"]
}

deny contains {"resource": rc.address, "reason": "wildcard grants unbounded access"} if {
	some rc in input.resource_changes
	rc.type == "kafka_acl"
	contains(rc.change.after.acl_principal, "*")
}
```

`input` is the plan JSON from `tofu show -json`. Every judgement needs a
`resource` to attach to and a `reason` — the reason is what the reviewer reads,
so both are required rather than optional.

When several rules in the same layer match one change, **the most severe wins**,
so adding a rule to a layer can only make it stricter. Across layers a higher
weight overrides a lower one — see [Layered policies](layered-policies.md).

!!! note "Rego v1 syntax"

    OPA 1.x parses Rego v1, so policies need the modern keywords — `contains`
    and `if`, with `some ... in` rather than `[_]`. A rule written in v0 syntax
    fails to compile rather than silently matching nothing.

See [the examples directory](https://github.com/raccoon-core/blastdoor/tree/main/examples)
for a worked policy and a plan per scenario, and [Worked examples](examples.md)
for how to iterate on one.

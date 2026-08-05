# Yuanshu Protocol 1.1

Protocol 1.1 is the Agent-neutral task surface used by new Yuanshu Nodes and
control clients. Protocol 1.0 remains available during migration for the
default managed Codex instance.

Important boundaries:

- `taskId` is a Node-generated opaque Yuanshu identifier.
- Native Agent session identifiers never appear in a 1.1 frame.
- `agentInstanceId` selects a configured Node-local Agent instance.
- Mutating Run and Interaction controls require a valid task lease.
- `reasoning.summary.*` contains only summaries explicitly emitted by an Agent;
  hidden reasoning is never requested, inferred, or exposed.
- Control sequence replay protection is shared with Protocol 1.0 for the same
  Owner, Node, Control Client, and key.
- Events use the independent `node-events-v1.1` stream and cursor.

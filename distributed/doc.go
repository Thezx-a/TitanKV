// Package distributed provides a minimal, teaching-oriented hashicorp/raft
// single-replica group for TitanKV.
//
// Honest scope (read this before citing the project in interviews):
//   - This is a 1-node Raft bootstrap for learning Apply / Snapshot / Restore.
//   - It is NOT a multi-node production consensus deployment (no join/leave,
//     no real quorum across hosts, Inmem log store by default).
//   - Optional MINIKV_ADDR forwarding is a best-effort side effect after the
//     in-memory FSM Apply; Raft correctness does not depend on MiniKV.
package distributed

# 🚀 Raft3D – Distributed 3D Print Job Management System

Raft3D is a distributed backend service designed to manage 3D printers, filaments, and print jobs across multiple networked nodes. It uses the **Raft Consensus Algorithm** to ensure strong consistency, fault tolerance, and automatic leader election.

The project simulates a fully decentralized system capable of recovering from node failures, synchronizing state using log replication and snapshots, and exposing a REST API to manage operations.

---

## ⚙️ Technologies Used

- **Language**: Go
- **Consensus Protocol**: [HashiCorp Raft](https://github.com/hashicorp/raft)
- **Storage Engine**: BoltDB
- **Transport**: TCP-based Raft Transport
- **API Testing**: Postman
- **Snapshotting**: Custom FSM snapshot & restore logic

---

## 📦 Build Instructions

### 1. Initialize Go Module
```bash
go mod init raft3d
go get github.com/hashicorp/raft@v1.7.3
go get github.com/hashicorp/raft-boltdb
go get github.com/google/uuid
go mod tidy

2. Build

go build -o raft3d main.go

🔗 Cluster Setup (3 Nodes)

Start three terminals and run:
🟢 Node 1 (Bootstrap)

./raft3d -id=node1 -http=127.0.0.1:8001 -raft=127.0.0.1:9001 -data=./data/node1 -bootstrap

🟡 Node 2 (Join)

./raft3d -id=node2 -http=127.0.0.1:8002 -raft=127.0.0.1:9002 -data=./data/node2 -join=127.0.0.1:8001

🟣 Node 3 (Join)

./raft3d -id=node3 -http=127.0.0.1:8003 -raft=127.0.0.1:9003 -data=./data/node3 -join=127.0.0.1:8001

🌐 API Endpoints
🎯 Printers

    POST /api/v1/printers

    GET /api/v1/printers

🎯 Filaments

    POST /api/v1/filaments

    GET /api/v1/filaments

🎯 Print Jobs

    POST /api/v1/print_jobs

    GET /api/v1/print_jobs

🎯 Job Status Update

    POST /api/v1/print_jobs/{id}/status?status=Running

    POST /api/v1/print_jobs/{id}/status?status=Done

🛠️ Utilities

    POST /debug/snapshot – trigger manual FSM snapshot

    GET /metrics – expose node stats and Raft state

🧠 Internal Architecture

    Raft Cluster: Handles leader election, log replication, and fault tolerance

    FSM (Finite State Machine): Applies logs to in-memory state

    Snapshotting: Saves FSM state periodically to compact logs and enable fast recovery

    Restore: Reloads FSM state from latest snapshot upon node restart

🧪 Testing Fault Tolerance

    Start all 3 nodes.

    Create printers or jobs using POST on any node (preferably the leader).

    Kill the leader node (Ctrl+C).

    Watch Raft elect a new leader.

    Continue operations from any remaining node.

    Restart the killed node — it syncs up automatically using Raft logs or snapshot restore.

📌 Notes

    All API writes (POST) must go to the current leader node

    Reads (GET) can be made from any node

    Snapshots and restore logic are built into the FSM using json encoding

✅ Conclusion

Raft3D demonstrates how to build a distributed, consistent, fault-tolerant backend system using Raft consensus.
It is fully operational across multiple nodes, supports dynamic leader election, state replication, recovery via snapshotting, and exposes a clean REST API for real-world 3D print job management.

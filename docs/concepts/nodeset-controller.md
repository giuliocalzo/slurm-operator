# NodeSet Controller

## Table of Contents

<!-- mdformat-toc start --slug=github --no-anchors --maxlevel=6 --minlevel=1 -->

- [NodeSet Controller](#nodeset-controller)
  - [Table of Contents](#table-of-contents)
  - [Overview](#overview)
  - [Design](#design)
    - [Sequence Diagram](#sequence-diagram)

<!-- mdformat-toc end -->

## Overview

The nodeset controller is responsible for managing and reconciling the NodeSet
CRD, which represents a set of homogeneous Slurm Nodes.

## Design

This controller is responsible for managing and reconciling the NodeSet CRD. In
addition to the regular responsibility of managing resources in Kubernetes via
the Kubernetes API, this controller should take into consideration the state of
Slurm to make certain reconciliation decisions.

### Sequence Diagram

```mermaid
sequenceDiagram
    autonumber

    actor User as User
    participant KAPI as Kubernetes API
    participant NS as NodeSet Controller
    box Operator Internals
        participant SCM as Slurm Client Map
        participant SEC as Slurm Event Channel
    end %% Operator Internals
    participant SC as Slurm Client
    participant SAPI as Slurm REST API

    loop Watch Slurm Nodes
        SC->>+SAPI: Get Slurm Nodes
        SAPI-->>-SC: Return Slurm Nodes
        SC->>SEC: Add Event for Cache Delta
    end %% loop Watch Slurm Nodes

    note over KAPI: Handle CR Update
    SEC-->>NS: Watch Event Channel
    User->>KAPI: Update NodeSet CR
    KAPI-->>NS: Watch NodeSet CRD
    opt Scale-out Replicas
        NS->>KAPI: Create Pods
    end %% Scale-out Replicas
    opt Scale-in Replicas
        SCM-->>NS: Lookup Slurm Client
        NS->>+SC: Drain Slurm Node
        SC->>+SAPI: Drain Slurm Node
        SAPI-->>-SC: Return Drain Slurm Node Status
        SC-->>-NS: Drain Slurm Node
        alt Slurm Node is Drained
            NS->>KAPI: Delete Pod
        else
            NS->>NS: Check Again Later
        end %% alt Slurm Node is Drained
    end %% opt Scale-in Replicas
```
### UseNodeNameAsHostname

When `spec.useNodeNameAsHostname` is enabled on a NodeSet, the controller
ensures each pod's hostname matches the Kubernetes node name it runs on. Because
Kubernetes only allows setting `spec.hostname` at pod creation time and the node
is only known after scheduling, the controller uses a two-phase approach:

1. Pods are created without a fixed hostname and are scheduled normally by the
   Kubernetes scheduler.
2. Once a pod is placed on a node, the controller detects when the pod hostname
   does not match the node name. It then deletes the pod and recreates it on the
   same node with `spec.hostname` and `spec.nodeName` both set to the
   scheduler-chosen node name.

This keeps scheduling decisions with the Kubernetes scheduler while allowing
Slurm node names to align with physical or virtual machine names (e.g. for
hostname-based resolution or topology).